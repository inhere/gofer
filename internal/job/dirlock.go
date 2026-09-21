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

// dirWaiter is one queued exclusive job: the directory it wants (already
// normalized), the job id, and the channel closed when it is granted the lock.
type dirWaiter struct {
	dir      string
	jobID    string
	granted  bool
	ready    chan struct{}
	holderAt string // who held the directory when this waiter queued (for reporting)
}

// newDirLocks builds an empty lock table (one per Service: the locks are
// process-scoped, on the machine that actually runs the jobs).
func newDirLocks() *dirLocks {
	return &dirLocks{held: map[string]string{}}
}

// Acquire takes the exclusive directory lock for dir on behalf of jobID, waiting
// (FIFO) while any held or already-queued directory overlaps it. onWait, when
// non-nil, is called ONCE with the blocking job id the moment this job is queued and
// is about to block — the caller uses it to make the wait OBSERVABLE (the job's
// `waiting_dir` state + event) while it is happening; a job that is granted at once
// never calls it. It returns:
//
//   - release: idempotent, ALWAYS non-nil on success — call it once the job's process
//     is over and BEFORE the terminal state becomes observable (a workflow step
//     advancing out of finish() may submit a job that wants this very directory);
//   - holder: the job id that blocked this one ("" when it was granted at once), so
//     the caller can tell the user WHO it is waiting for;
//   - waited: whether it actually had to queue;
//   - err: ctx's error when the wait was cancelled (no lock is taken, nothing to
//     release).
//
// onWait is what the design's "waiting_dir must be observable" requirement costs: a
// return-value-only API cannot report anything until the wait is already over.
func (d *dirLocks) Acquire(ctx context.Context, dir, jobID string, onWait func(holder string)) (release func(), holder string, waited bool, err error) {
	if dir == "" {
		// A job with no local working directory (a remote one: the path belongs to
		// another machine) has nothing to serialize here.
		return func() {}, "", false, nil
	}
	key := lockDirKey(dir)

	d.mu.Lock()
	if !d.conflictsLocked(key) {
		d.held[key] = jobID
		d.mu.Unlock()
		return d.releaser(key, jobID), "", false, nil
	}
	w := &dirWaiter{
		dir:      key,
		jobID:    jobID,
		ready:    make(chan struct{}),
		holderAt: d.firstHolderLocked(key),
	}
	d.waiters = append(d.waiters, w)
	d.mu.Unlock()

	if onWait != nil {
		onWait(w.holderAt)
	}

	select {
	case <-w.ready:
		// Granted (the releaser moved the key into `held` for us, honouring FIFO with
		// a possible wait behind other waiters). `holderAt` is who blocked us then.
		return d.releaser(key, jobID), w.holderAt, true, nil
	case <-ctx.Done():
		if d.abandon(w) {
			// Removed from the queue before it was granted: no lock to release.
			return func() {}, "", true, ctx.Err()
		}
		// Raced with the grant pass: we HOLD the lock now, so hand it straight back
		// (the caller is not going to use it) instead of leaking a directory forever.
		d.releaser(key, jobID)()
		return func() {}, w.holderAt, true, ctx.Err()
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

// releaser returns the idempotent release func for key held by jobID: it drops the
// lock, then hands it to whatever the queue now allows (FIFO).
func (d *dirLocks) releaser(key, jobID string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			d.mu.Lock()
			defer d.mu.Unlock()
			if cur, ok := d.held[key]; ok && cur == jobID {
				delete(d.held, key)
			}
			d.grantLocked()
		})
	}
}

// grantLocked walks the FIFO queue and grants every waiter whose directory no
// longer conflicts with the HELD set. Earlier grants of this very pass are added to
// `held` as they are made, so the pass is by construction FIFO-correct: a later
// waiter whose directory overlaps an earlier grant keeps waiting (it must not be
// granted while its neighbour is about to run), and no waiter can ever be compared
// against itself — which is why the pass never consults the queue. Caller holds mu.
func (d *dirLocks) grantLocked() {
	kept := d.waiters[:0]
	for _, w := range d.waiters {
		if d.conflictsHeldLocked(w.dir) {
			kept = append(kept, w)
			continue
		}
		w.granted = true
		d.held[w.dir] = w.jobID
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

// conflictsLocked reports whether dir overlaps anything held or anything already
// queued. Queued waiters count too: a newcomer that clashes with someone who is
// ahead in line waits behind them (strict FIFO), instead of slipping past a job that
// was only blocked by a different holder.
func (d *dirLocks) conflictsLocked(dir string) bool {
	if d.conflictsHeldLocked(dir) {
		return true
	}
	for _, w := range d.waiters {
		if dirsOverlap(dir, w.dir) {
			return true
		}
	}
	return false
}

// firstHolderLocked names the job blocking dir (the first overlapping holder), or ""
// when nothing held overlaps it (then a QUEUED waiter is what blocks it, and the
// caller is told a queue — not a specific job — is ahead).
func (d *dirLocks) firstHolderLocked(dir string) string {
	for held, id := range d.held {
		if dirsOverlap(dir, held) {
			return id
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
