package sessionrelay

import (
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

const anonPrefix = "?anon-"

// subagentTracker keeps the in-flight sub-agents per session. It is in-memory on
// purpose: the state is short-lived (bounded by SubagentExpiry), and losing it on
// a server restart only means auto may arm during a sub-agent run — the previous
// behaviour — with no schema to migrate.
type subagentTracker struct {
	mu   sync.Mutex
	sess map[string]map[string]int64 // sid -> agent id -> last event unix seconds
	anon int64
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

// apply records one event and returns the live count afterwards. delta > 0 adds
// (or refreshes) the id, delta < 0 removes it. An empty id is tolerated: a start
// gets a synthetic id, a stop removes the oldest synthetic one.
func (t *subagentTracker) apply(sid, id string, delta int, now int64) int {
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
		delete(m, id)
		if len(m) == 0 {
			delete(t.sess, sid)
		}
	}
	return len(t.sess[sid])
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
// explicit `on` switch is the human's authoritative "wait for me" and is left to
// its own budget. The delta wins over the event name when both are present.
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
	left := s.subagents.apply(sid, in.SubagentID, delta, s.nowFn().Unix())
	if delta >= 0 || left > 0 {
		return nil
	}
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil || !ok || a.RelayMode == jobstore.RelayModeOn {
		return err
	}
	open, err := s.store.ListSessionDecisions(sid, jobstore.DecisionOpen, 20, "")
	if err != nil {
		return err
	}
	released := false
	for _, d := range open.Decisions {
		done, err := s.store.ReleaseDecision(d.ID, ReleaseBySubagentDone)
		if err != nil {
			return err
		}
		released = released || done
	}
	if released && a.State == jobstore.SessionWaitingReply {
		_, _ = s.store.SetSessionState(sid, jobstore.SessionIdle)
	}
	return nil
}
