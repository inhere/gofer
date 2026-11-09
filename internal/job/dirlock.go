package job

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/inhere/gofer/internal/util"
)

// dirLocks serializes the jobs that must not share a working directory (JOB-11).
//
// Only EXCLUSIVE jobs take a lock; a shared one neither takes it nor is blocked by
// it. The conflict rule is directory OVERLAP, not equality: a job in `proj` and one
// in `proj/sub` edit the same checkout, so they are the same lock. Paths are
// compared through util.RealPath (symlinks, and on Windows 8.3 short names) and with
// filepath.Rel, which compares case-insensitively on Windows — the two names must
// denote the same directory, not merely look alike.
//
// Waiting is FIFO: a job that has to queue goes to the back, and every waiter ahead
// of it that can be granted is granted first (the grant pass walks the queue in
// order). A cancelled waiter is simply removed; it never blocks the jobs behind it.
type dirLocks struct {
	mu   sync.Mutex
	held map[string]string // real dir -> job id currently holding it
	// waiters is the FIFO queue of jobs parked on a conflict. Each has its own ready
	// channel because the wait must be select-able against ctx (sync.Cond cannot be).
	waiters []*dirWaiter
}

// dirWaiter is one queued exclusive job: ALL the directories it wants (already
// normalized; granted together or not at all), the job id, and the channel closed when
// it is granted the lock set.
type dirWaiter struct {
	dirs     []string
	jobID    string
	granted  bool
	ready    chan struct{}
	holderAt string // who held a wanted directory when this waiter queued (for reporting)
}

// newDirLocks builds an empty lock table (one per Service: the locks are
// process-scoped, on the machine that actually runs the jobs).
func newDirLocks() *dirLocks {
	return &dirLocks{held: map[string]string{}}
}

// Acquire takes the exclusive directory lock for ALL of dirs on behalf of jobID,
// waiting (FIFO) while any held or already-queued directory overlaps any of them. The
// set is ONE waiter: it is granted all at once and released all at once, so a job never
// holds some paths while waiting for the others (two jobs declaring [a,b] and [b,a]
// cannot deadlock each other). onWait, when non-nil, is called ONCE with the blocking
// job id and the wanted path that is blocked, the moment this job is queued and is about
// to block — the caller uses it to make the wait OBSERVABLE (the job's `waiting_dir`
// state + event) while it is happening; a job that is granted at once never calls it.
// It returns:
//
//   - release: idempotent, ALWAYS non-nil on success — call it once the job's process
//     is over and BEFORE the terminal state becomes observable (a workflow step
//     advancing out of finish() may submit a job that wants this very directory);
//   - holder: the job id that blocked this one ("" when it was granted at once), so
//     the caller can tell the user WHO it is waiting for. On the cancelled path it is
//     the holder that was blocking the job when it queued (`holderAt`) — which is
//     what the caller needs to explain the wait in its event;
//   - waited: whether it actually had to queue;
//   - err: ctx's error when the wait was cancelled (no lock is taken, nothing to
//     release).
//
// onWait is what the design's "waiting_dir must be observable" requirement costs: a
// return-value-only API cannot report anything until the wait is already over.
func (d *dirLocks) Acquire(ctx context.Context, dirs []string, jobID string, onWait func(holder, dir string)) (release func(), holder string, waited bool, err error) {
	// Empty entries (a remote job's path belongs to another machine) have nothing to
	// serialize here; orig keeps the caller's spelling for reporting.
	var keys, orig []string
	for _, dir := range dirs {
		if dir != "" {
			keys = append(keys, lockDirKey(dir))
			orig = append(orig, dir)
		}
	}
	if len(keys) == 0 {
		return func() {}, "", false, nil
	}

	d.mu.Lock()
	blocked := d.blockedIndexLocked(keys)
	if blocked < 0 {
		d.holdLocked(keys, jobID)
		d.mu.Unlock()
		return d.releaser(keys, jobID), "", false, nil
	}
	w := &dirWaiter{
		dirs:     keys,
		jobID:    jobID,
		ready:    make(chan struct{}),
		holderAt: d.firstHolderLocked(keys),
	}
	d.waiters = append(d.waiters, w)
	d.mu.Unlock()

	if onWait != nil {
		onWait(w.holderAt, orig[blocked])
	}
	return d.await(ctx, w, keys)
}

// await parks on a queued waiter until it is granted or ctx ends.
func (d *dirLocks) await(ctx context.Context, w *dirWaiter, keys []string) (release func(), holder string, waited bool, err error) {
	select {
	case <-w.ready:
		// Granted (the releaser moved the keys into `held` for us, honouring FIFO with
		// a possible wait behind other waiters). `holderAt` is who blocked us then.
		return d.releaser(keys, w.jobID), w.holderAt, true, nil
	case <-ctx.Done():
		if d.abandon(w) {
			// Removed from the queue before it was granted: no lock to release.
			return func() {}, w.holderAt, true, ctx.Err()
		}
		// Raced with the grant pass: we HOLD the locks now, so hand them straight back
		// (the caller is not going to use them) instead of leaking a directory forever.
		d.releaser(keys, w.jobID)()
		return func() {}, w.holderAt, true, ctx.Err()
	}
}

// holdLocked records every key as held by jobID. Caller holds mu.
func (d *dirLocks) holdLocked(keys []string, jobID string) {
	for _, k := range keys {
		d.held[k] = jobID
	}
}

// abandon removes a cancelled waiter from the queue, reporting whether it was still
// queued (true) or had already been granted (false).
func (d *dirLocks) abandon(w *dirWaiter) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if w.granted {
		return false
	}
	for i, cur := range d.waiters {
		if cur == w {
			d.waiters = append(d.waiters[:i], d.waiters[i+1:]...)
			return true
		}
	}
	// Not queued and not granted: unreachable (the grant pass only ever grants
	// queued waiters, and abandon is the only other remover).
	return false
}

// releaser returns the idempotent release func for the keys held by jobID: it drops
// them all, then hands them to whatever the queue now allows (FIFO).
func (d *dirLocks) releaser(keys []string, jobID string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			d.mu.Lock()
			defer d.mu.Unlock()
			for _, key := range keys {
				if cur, ok := d.held[key]; ok && cur == jobID {
					delete(d.held, key)
				}
			}
			d.grantLocked()
		})
	}
}

// grantLocked walks the FIFO queue and grants every waiter whose directories no
// longer conflict with the HELD set. Earlier grants of this very pass are added to
// `held` as they are made, so the pass is by construction FIFO-correct: a later
// waiter whose directory overlaps an earlier grant keeps waiting (it must not be
// granted while its neighbour is about to run), and no waiter can ever be compared
// against itself — which is why the pass never consults the queue. Caller holds mu.
func (d *dirLocks) grantLocked() {
	kept := d.waiters[:0]
	for _, w := range d.waiters {
		if d.anyConflictsHeldLocked(w.dirs) {
			kept = append(kept, w)
			continue
		}
		w.granted = true
		d.holdLocked(w.dirs, w.jobID)
		close(w.ready)
	}
	d.waiters = kept
}

// conflictsHeldLocked reports whether dir overlaps a directory that is currently
// held.
func (d *dirLocks) conflictsHeldLocked(dir string) bool {
	for held := range d.held {
		if dirsOverlap(dir, held) {
			return true
		}
	}
	return false
}

// anyConflictsHeldLocked reports whether any of dirs overlaps a held directory.
func (d *dirLocks) anyConflictsHeldLocked(dirs []string) bool {
	for _, dir := range dirs {
		if d.conflictsHeldLocked(dir) {
			return true
		}
	}
	return false
}

// blockedIndexLocked returns the index of the first of dirs that overlaps anything held
// or anything already queued, or -1 when none does. Queued waiters count too: a newcomer
// that clashes with someone who is ahead in line waits behind them (strict FIFO),
// instead of slipping past a job that was only blocked by a different holder.
func (d *dirLocks) blockedIndexLocked(dirs []string) int {
	for i, dir := range dirs {
		if d.conflictsHeldLocked(dir) {
			return i
		}
		for _, w := range d.waiters {
			for _, wd := range w.dirs {
				if dirsOverlap(dir, wd) {
					return i
				}
			}
		}
	}
	return -1
}

// firstHolderLocked names the job blocking dirs (the first overlapping holder), or ""
// when nothing held overlaps them (then a QUEUED waiter is what blocks, and the caller
// is told a queue — not a specific job — is ahead).
func (d *dirLocks) firstHolderLocked(dirs []string) string {
	for _, dir := range dirs {
		for held, id := range d.held {
			if dirsOverlap(dir, held) {
				return id
			}
		}
	}
	return ""
}

// lockDirKey normalizes a directory for comparison: the real (symlink- and
// short-name-resolved) absolute-ish path, cleaned. The job's cwd is already absolute
// (project.SafeJoin); RealPath additionally collapses the aliases a Windows/macOS
// checkout is full of (/tmp vs /private/tmp, RUNNER~1 vs runneradmin).
func lockDirKey(dir string) string {
	return util.RealPath(filepath.Clean(dir))
}

// dirsOverlap reports whether a and b are the same directory or one contains the
// other — the "same lock" relation of design §二. filepath.Rel compares path
// components (case-insensitively on Windows) and errors across volumes, which is
// exactly "no overlap".
func dirsOverlap(a, b string) bool {
	return dirContains(a, b) || dirContains(b, a)
}

// dirContains reports whether child is parent or lives inside it.
func dirContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
