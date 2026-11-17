package job

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/runner"
)

// Persistent sessions and the directory lock (R8). A session that waits for input
// gives its lock back, so a same-directory writable job is not queued behind an idle
// conversation; before the next turn starts it takes the lock again (queueing like any
// other job). The lock table itself is untouched: this file only changes WHEN a session
// holds its lock, through the table's ordinary Acquire / release.

// errSessionEndWhileDirWait is the cancel cause an `end` request gives to a session
// that is queued for its directory lock.
var errSessionEndWhileDirWait = errors.New("session ended while waiting for the directory lock")

// dirGrantLogMax bounds the passive grant log.
const dirGrantLogMax = 256

// dirGrant is one entry of the passive grant log.
type dirGrant struct {
	seq   uint64
	jobID string
	keys  []string
}

// recordGrantLocked appends to the grant log. Caller holds mu.
func (d *dirLocks) recordGrantLocked(keys []string, jobID string) {
	d.seq++
	d.grants = append(d.grants, dirGrant{seq: d.seq, jobID: jobID, keys: append([]string(nil), keys...)})
	if over := len(d.grants) - dirGrantLogMax; over > 0 {
		d.grants = append([]dirGrant(nil), d.grants[over:]...)
	}
}

// mark is the current position of the grant log.
func (d *dirLocks) mark() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.seq
}

// usersSince lists (in grant order, once each) the jobs other than self that were
// granted a lock overlapping keys after mark.
func (d *dirLocks) usersSince(mark uint64, keys []string, self string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	seen := map[string]bool{}
	for _, g := range d.grants {
		if g.seq <= mark || g.jobID == self || seen[g.jobID] {
			continue
		}
		if keysOverlap(keys, g.keys) {
			seen[g.jobID] = true
			out = append(out, g.jobID)
		}
	}
	return out
}

func keysOverlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if dirsOverlap(x, y) {
				return true
			}
		}
	}
	return false
}

// sessionDirHold is the directory-lock handle of one persistent session job.
type sessionDirHold struct {
	s     *Service
	entry *jobEntry
	locks *dirLocks
	ctx   context.Context // the job's queueing context (cancel = job cancel)
	dirs  []string
	jobID string
	wait  int // server.dir_lock_max_wait_sec for the re-acquire (0 = no cap)

	mu         sync.Mutex // never held while waiting for the lock or while holding entry.mu
	release    func()     // nil while the lock is given back
	yielded    bool
	closed     bool
	mark       uint64
	waiting    bool
	cancelWait context.CancelCauseFunc
	note       string
}

// attachSessionDirHold makes the lock just taken by execute a session-scoped one. It
// returns nil (the lock stays held for the whole job, as for any other job) unless the
// job is a local persistent session that did not ask to keep its lock.
func (s *Service) attachSessionDirHold(entry *jobEntry, ctx context.Context, gates execGates, req runner.Request, dirs []string, release func()) *sessionDirHold {
	if gates.keepDirLock || entry.sessionCommands == nil || req.ACP == nil {
		return nil
	}
	entry.mu.Lock()
	session := entry.result.Session
	entry.mu.Unlock()
	if !session {
		return nil
	}
	h := &sessionDirHold{s: s, entry: entry, locks: s.dirLock, ctx: ctx, dirs: dirs, jobID: req.JobID, wait: gates.dirWait, release: release}
	entry.dirHold.Store(h)
	return h
}

// yield gives the lock back (the session is waiting for input).
func (h *sessionDirHold) yield() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.release == nil || h.closed {
		return
	}
	h.mark = h.locks.mark() // before the release: it may grant a waiter at once
	h.release()
	h.release, h.yielded = nil, true
}

// reacquire takes the lock again before a turn starts, queueing like any job. It
// holds neither entry.mu nor h.mu while queued and is cancellable by the job's cancel,
// an end request and the wait cap.
func (h *sessionDirHold) reacquire() error {
	h.mu.Lock()
	if h.release != nil || !h.yielded {
		h.mu.Unlock()
		return nil
	}
	if h.closed {
		h.mu.Unlock()
		return context.Canceled
	}
	waitCtx, cancel := context.WithCancelCause(h.ctx)
	stop := context.CancelFunc(func() { cancel(nil) })
	if h.wait > 0 {
		var cancelT context.CancelFunc
		waitCtx, cancelT = context.WithTimeout(waitCtx, time.Duration(h.wait)*time.Second)
		stop = func() { cancelT(); cancel(nil) }
	}
	h.cancelWait, h.waiting = cancel, true
	mark := h.mark
	h.mu.Unlock()
	defer stop()

	release, holder, _, err := h.locks.Acquire(waitCtx, h.dirs, h.jobID, func(holder, dir string) {
		h.entry.mu.Lock()
		turn := h.entry.result.TurnNo + 1
		h.entry.mu.Unlock()
		h.s.recordEvent(h.jobID, EventJobDirWait, map[string]any{"holder_job": holder, "dir": dir, "turn_no": turn})
	})

	h.mu.Lock()
	h.waiting, h.cancelWait = false, nil
	if err != nil {
		h.mu.Unlock()
		if errors.Is(context.Cause(waitCtx), errSessionEndWhileDirWait) {
			return runner.ErrSessionEndRequested
		}
		if h.ctx.Err() != nil {
			return h.ctx.Err()
		}
		// The wait cap ran out: the message is NOT sent and the session goes back to
		// awaiting_input (the person can send it again); it is never failed for this.
		lockPathText := strings.Join(h.dirs, ", ")
		h.s.recordEvent(h.jobID, EventJobDirWaitTimeout, map[string]any{"holder_job": holder, "dir": lockPathText, "wait_sec": h.wait, "unsent": true})
		return &runner.TurnNotStartedError{Reason: i18n.T(h.lang(), "job.dir_wait.unsent", "holder", holder)}
	}
	if h.closed {
		h.mu.Unlock()
		release()
		return context.Canceled
	}
	h.release, h.yielded = release, false
	h.note = h.changeNote(mark)
	h.mu.Unlock()
	return nil
}

// abortWait ends a pending lock wait because the session was ended; false when the
// session is not waiting for the lock.
func (h *sessionDirHold) abortWait() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.waiting || h.cancelWait == nil {
		return false
	}
	h.cancelWait(errSessionEndWhileDirWait)
	return true
}

// releaseAll gives the lock up for good (the job is over).
func (h *sessionDirHold) releaseAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	if h.release != nil {
		h.release()
		h.release = nil
	}
}

// takeNote returns (once) the "the directory was used meanwhile" note for the turn
// that is starting.
func (h *sessionDirHold) takeNote() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := h.note
	h.note = ""
	return n
}

// changeNote renders, in the injected-section format (the same frame as the rules, so
// readers fold it away), the jobs that used the directory while the lock was given back.
func (h *sessionDirHold) changeNote(mark uint64) string {
	keys := make([]string, 0, len(h.dirs))
	for _, d := range h.dirs {
		if d != "" {
			keys = append(keys, lockDirKey(d))
		}
	}
	users := h.locks.usersSince(mark, keys, h.jobID)
	if len(users) == 0 {
		return ""
	}
	parts := make([]string, 0, len(users))
	for _, id := range users {
		if r, ok := h.s.Get(id); ok && strings.TrimSpace(r.Title) != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", id, strings.TrimSpace(r.Title)))
		} else {
			parts = append(parts, id)
		}
	}
	lang := h.lang()
	name := i18n.T(lang, "job.dir_wait.title")
	body := i18n.T(lang, "job.dir_wait.changed", "jobs", strings.Join(parts, ", "))
	return renderRulesSectionIn(lang, []RuleRef{{Name: name}}, map[string]string{name: body})
}

// lang is the language of the session's project.
func (h *sessionDirHold) lang() i18n.Lang {
	cfg := h.s.cfg.Load()
	if cfg == nil {
		return i18n.Default
	}
	h.entry.mu.Lock()
	key := h.entry.result.ProjectKey
	h.entry.mu.Unlock()
	return cfg.LanguageFor(key)
}
