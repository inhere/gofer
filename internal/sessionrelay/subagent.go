package sessionrelay

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// Sub-agent awareness (N1 §C, SESS-10). Claude Code raises SubagentStart /
// SubagentStop hooks around every sub-agent; the hook reports them as heartbeat
// events with a +1/-1 delta and the sub-agent's id. A session with sub-agents in
// flight is being supervised just like one with live jobs (SUP-01 D): `auto` does
// not arm, and when the last sub-agent finishes while a Stop hook is already
// blocked on an auto-armed wait, that wait is released so the main agent can
// consume the result.
const (
	EventSubagentStart = "SubagentStart"
	EventSubagentStop  = "SubagentStop"
)

// ReleaseBySubagentDone tags a turn closed without an answer because the
// session's last running sub-agent finished (the blocked Stop hook lets the main
// agent go on).
const ReleaseBySubagentDone = "subagent_done"

// SubagentExpiry is how long a sub-agent counts as running without any further
// event for it: a lost SubagentStop must not keep a session unarmed forever.
const SubagentExpiry = 2 * time.Hour

// SubagentReleaseTTL is how long a "released by a finished sub-agent" mark outlives
// its release when no new turn ever arrives: the mark only explains an idle session,
// so it must not stay on forever.
const SubagentReleaseTTL = time.Hour

const anonPrefix = "?anon-"

// subagentTracker keeps the in-flight sub-agents per session. It is in-memory on
// purpose: the state is short-lived (bounded by SubagentExpiry), and losing it on
// a server restart only means auto may arm during a sub-agent run — the previous
// behaviour — with no schema to migrate.
type subagentTracker struct {
	mu   sync.Mutex
	sess map[string]map[string]int64 // sid -> agent id -> last event unix seconds
	anon int64
	// released: sid -> unix seconds when the last sub-agent's end let go of a Stop wait
	// (gofer-m80j). Cleared by the next turn event (UserPromptSubmit / Stop / ...).
	released map[string]int64
}

func (t *subagentTracker) markReleased(sid string, now int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.released == nil {
		t.released = make(map[string]int64)
	}
	t.released[sid] = now
}

func (t *subagentTracker) clearReleased(sid string) {
	t.mu.Lock()
	delete(t.released, sid)
	t.mu.Unlock()
}

func (t *subagentTracker) releasedAt(sid string, now int64) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	at, ok := t.released[sid]
	if !ok {
		return 0
	}
	if now-at > int64(SubagentReleaseTTL/time.Second) {
		delete(t.released, sid)
		return 0
	}
	return at
}

// prune drops expired entries of sid and returns what is left (nil = none).
func (t *subagentTracker) prune(sid string, now int64) map[string]int64 {
	m := t.sess[sid]
	for id, at := range m {
		if now-at > int64(SubagentExpiry/time.Second) {
			delete(m, id)
		}
	}
	if len(m) == 0 {
		delete(t.sess, sid)
		return nil
	}
	return m
}

// apply records one event and returns the live count afterwards, and whether a stop
// ended a sub-agent this tracker knew (a stop with no matching start — Claude Code
// raises SubagentStop for side queries it never announced — is not "one finished"). delta > 0 adds
// (or refreshes) the id, delta < 0 removes it. An empty id is tolerated: a start
// gets a synthetic id, a stop removes the oldest synthetic one.
func (t *subagentTracker) apply(sid, id string, delta int, now int64) (live int, ended bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sess == nil {
		t.sess = make(map[string]map[string]int64)
	}
	m := t.prune(sid, now)
	switch {
	case delta > 0:
		if m == nil {
			m = make(map[string]int64)
			t.sess[sid] = m
		}
		if id == "" {
			t.anon++
			id = anonPrefix + strconv.FormatInt(t.anon, 10)
		}
		m[id] = now
	case delta < 0:
		if id == "" {
			oldest, oldestAt := "", int64(0)
			for k, at := range m {
				if strings.HasPrefix(k, anonPrefix) && (oldest == "" || at < oldestAt) {
					oldest, oldestAt = k, at
				}
			}
			id = oldest
		}
		if _, ok := m[id]; ok {
			ended = true
			delete(m, id)
		}
		if len(m) == 0 {
			delete(t.sess, sid)
		}
	}
	return len(t.sess[sid]), ended
}

func (t *subagentTracker) count(sid string, now int64) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.prune(sid, now))
}

func (t *subagentTracker) clear(sid string) {
	t.mu.Lock()
	delete(t.sess, sid)
	t.mu.Unlock()
}

// SubagentCount is the number of sub-agents currently running in the session.
func (s *Service) SubagentCount(sid string) int {
	return s.subagents.count(sid, s.nowFn().Unix())
}

// SubagentReleasedAt is when a Stop wait of the session was let go because its last
// sub-agent finished, as long as no later turn event (a prompt, the next Stop) has
// proved the main agent really got a new turn out of it; 0 otherwise. Such a session
// shows as plain idle although relay is on and nothing is waiting (gofer-m80j), so the
// session view names the reason and a web answer is not refused (SayReleased).
func (s *Service) SubagentReleasedAt(sid string) int64 {
	return s.subagents.releasedAt(sid, s.nowFn().Unix())
}

// clearsSubagentRelease reports whether a hook event shows the main agent running a
// turn again (or gone), which ends the "released by a sub-agent" story.
func clearsSubagentRelease(event string) bool {
	switch event {
	case EventUserPromptSubmit, EventStop, EventSessionStart, EventSessionEnd, EventInterrupt:
		return true
	}
	return false
}

// SayReleased is the answer path for a web text aimed at the open turn of a session whose
// turn a finished sub-agent just released (gofer-m80j): the turn is gone, nobody waits,
// and Say would refuse the text. Such a text goes out through the message ladder instead,
// which starts a new turn (and the Stop that ends it waits again). handled is false when
// the session is not in that state, so the caller keeps its own "no open turn" answer.
func (s *Service) SayReleased(ctx context.Context, sid, text, operator, origin string) (m jobstore.SessionMessage, handled bool, err error) {
	if s.SubagentReleasedAt(sid) == 0 {
		return jobstore.SessionMessage{}, false, nil
	}
	m, err = s.SendMessage(ctx, sid, text, operator, origin)
	return m, true, err
}

// SetWaitBudgets sets the Stop-wait budgets handed to the hook as wait_budget_sec:
// onSec for an explicit `on` switch, autoSec for an auto-armed wait (0 = no cap).
func (s *Service) SetWaitBudgets(onSec, autoSec int) {
	atomic.StoreInt64(&s.waitOnSec, int64(onSec))
	atomic.StoreInt64(&s.waitAutoSec, int64(autoSec))
}

// WaitBudgetSec is the budget for a wait of the given reason (see WaitReason):
// 0 when the session does not wait or no cap is configured.
func (s *Service) WaitBudgetSec(reason string) int {
	switch reason {
	case "":
		return 0
	case WaitModeOn:
		return int(atomic.LoadInt64(&s.waitOnSec))
	}
	return int(atomic.LoadInt64(&s.waitAutoSec))
}

// noteSubagent applies a SubagentStart / SubagentStop beat and, when the last
// sub-agent just finished, releases the session's auto-armed OPEN turns. An
// switch `on` is released too (gofer-7lac follow-up): a web message turns the switch on,
// and leaving the turn open parked the main agent with a finished sub-agent's result
// undelivered until the person typed in the terminal. The agent handles the result and
// its next Stop waits again, so a web reply is not lost. The delta wins over the event
// name when both are present.
func (s *Service) noteSubagent(sid string, in HeartbeatInput) error {
	delta := in.SubagentDelta
	if delta == 0 {
		switch in.Event {
		case EventSubagentStart:
			delta = 1
		case EventSubagentStop:
			delta = -1
		}
	}
	left, ended := s.subagents.apply(sid, in.SubagentID, delta, s.nowFn().Unix())
	// An unpaired SubagentStop (no tracked start for that id) neither counts nor
	// releases: the main agent's wait must not end because of an event nobody started
	// (gofer-m80j).
	if delta >= 0 || !ended || left > 0 {
		return nil
	}
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil || !ok {
		return err
	}
	// Only relay turns: a permission prompt of the main agent is still on screen.
	open, err := s.store.ListOpenSessionTurns(sid, 20)
	if err != nil {
		return err
	}
	released := false
	for _, d := range open {
		done, err := s.store.ReleaseDecision(d.ID, ReleaseBySubagentDone)
		if err != nil {
			return err
		}
		released = released || done
	}
	if released {
		s.subagents.markReleased(sid, s.nowFn().Unix())
	}
	if released && a.State == jobstore.SessionWaitingReply {
		_, _ = s.store.SetSessionState(sid, jobstore.SessionIdle)
	}
	return nil
}
