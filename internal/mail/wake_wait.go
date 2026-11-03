package mail

import (
	"log/slog"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/logx"
)

// Why a session with pending letters is still waiting for its wake (mail.wake_waiting). The
// wait is the deliverer's decision (WakeDue + wakeAllowed), never a jump — without a line here
// "the letter arrived three minutes late" has no explanation anywhere in the server log.
const (
	// WaitNotIdleLongEnough: the session is idle / offline but its last hook beat is younger
	// than mail.wake_idle_sec, so waking it now would interrupt a person who is right there.
	WaitNotIdleLongEnough = "not_idle_long_enough"
	// WaitWaitingReply: the session is blocked on a question for a person; the letters wait
	// for its next turn instead of starting a second one.
	WaitWaitingReply = "waiting_reply"
	// WaitPaused: the session's mail pause switch is on (delivery suppressed until resumed).
	WaitPaused = "paused"
	// WaitWakeInterval: the letters are wakeable, but the session was woken less than
	// mail.wake_interval_sec ago — a wake that is not answered must not be repeated per tick.
	WaitWakeInterval = "wake_interval"
	// waitStatePrefix is the reason of every other session state (running / ended /
	// handed_off / needs_attention): those are not woken at all (WakeDue), and the state
	// itself is the explanation. A running session takes the letters at its next turn.
	waitStatePrefix = "state_"
)

// maxWakeWaits bounds the deliverer's wake-waiting memory. Entries are dropped as soon as the
// session is woken or has no pending letters, so the table only holds sessions that are
// genuinely waiting; the cap is the last resort for a long-lived server.
const maxWakeWaits = 1024

// wakeWait is what one session was last reported waiting for.
type wakeWait struct {
	reason string
	// earliest is the expected wake time that was reported with reason: a wait whose
	// expected time moved LATER is news even when the reason did not change.
	earliest time.Time
	at       time.Time
}

// wakeWaitReason explains why a session with pending letters is not woken now.
func wakeWaitReason(a jobstore.AgentSession) string {
	switch a.State {
	case jobstore.SessionIdle, jobstore.SessionOffline:
		// Wakeable in principle, but not yet idle long enough (an urgent letter would have
		// woken it, so this session has only ordinary letters).
		return WaitNotIdleLongEnough
	case jobstore.SessionWaitingReply:
		return WaitWaitingReply
	}
	return waitStatePrefix + a.State
}

// wakeWaitEarliest is the earliest moment the wait can end by itself, as far as this sweep can
// tell (zero time = only an event can change it: a hook beat, a person's answer, a resume).
func wakeWaitEarliest(a jobstore.AgentSession, now time.Time, st Settings) time.Time {
	switch a.State {
	case jobstore.SessionIdle, jobstore.SessionOffline:
		if a.LastSeenAt > 0 {
			return time.Unix(a.LastSeenAt, 0).Add(st.WakeIdle)
		}
	}
	return time.Time{}
}

// noteWakeWaiting reports that a session's letters are still waiting — ONCE per state: a sweep
// runs every tick and "nothing changed" is not news. The entry is replaced when the reason
// changes OR when the expected wake time moves, so an operator sees the sequence
// (not_idle_long_enough → wake_interval → woke).
//
// The expected time has to be compared on its own: a hook keeps beating while the agent is
// active, every beat pushes last_seen_at (and with it the idle deadline) later, and the reason
// stays not_idle_long_enough the whole time. Reporting only once would leave the log pointing
// at a wake time that has long passed without a wake.
func (d *Deliverer) noteWakeWaiting(a jobstore.AgentSession, pending int, reason string, earliest, now time.Time) {
	if reason == "" || a.SessionID == "" {
		return
	}
	d.mu.Lock()
	if d.wakeWaits == nil {
		d.wakeWaits = make(map[string]wakeWait)
	}
	if prev, ok := d.wakeWaits[a.SessionID]; ok && prev.reason == reason && prev.earliest.Equal(earliest) {
		d.wakeWaits[a.SessionID] = wakeWait{reason: reason, earliest: earliest, at: now}
		d.mu.Unlock()
		return
	}
	if _, ok := d.wakeWaits[a.SessionID]; !ok && len(d.wakeWaits) >= maxWakeWaits {
		d.evictOldestWakeWaitLocked()
	}
	d.wakeWaits[a.SessionID] = wakeWait{reason: reason, earliest: earliest, at: now}
	d.mu.Unlock()
	attrs := []any{"session", a.SessionID, "pending", pending, "state", a.State, "reason", reason}
	if !earliest.IsZero() {
		attrs = append(attrs, "earliest_wake_at", earliest.Format(logx.TimeLayout))
	}
	slog.Info(EventWakeWaiting, attrs...)
}

// evictOldestWakeWaitLocked drops the longest-untouched entry (d.mu held).
func (d *Deliverer) evictOldestWakeWaitLocked() {
	var oldestKey string
	var oldest time.Time
	for k, v := range d.wakeWaits {
		if oldestKey == "" || v.at.Before(oldest) {
			oldestKey, oldest = k, v.at
		}
	}
	if oldestKey != "" {
		delete(d.wakeWaits, oldestKey)
	}
}

// dropWakeWait forgets a session: it was woken (or its letters went away).
func (d *Deliverer) dropWakeWait(sid string) {
	d.mu.Lock()
	delete(d.wakeWaits, sid)
	d.mu.Unlock()
}

// retainWakeWaits keeps only the sessions that still have pending letters, so a session that
// was delivered / cancelled / expired drops out of the memory on the next sweep instead of
// living until the server restarts.
func (d *Deliverer) retainWakeWaits(keep map[string]bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for sid := range d.wakeWaits {
		if !keep[sid] {
			delete(d.wakeWaits, sid)
		}
	}
}

// lastWakeAttempt is when the deliverer last woke a session (zero = never).
func (d *Deliverer) lastWakeAttempt(sid string) time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.wokeAt[sid]
}
