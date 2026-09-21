package job

// agenthealth.go turns the read-time health aggregate (agent.HealthState) into an EVENT
// when an agent's verdict CHANGES (design §六, AUTO-02b's neighbour): a provider that
// starts failing records agent.degraded, one that comes back records agent.recovered.
//
// Why an event and not just the badge: the aggregate is computed on every read, so a
// human only notices a provider going bad by looking. A webhook subscriber (an on-call
// channel, a bot that switches a runner off) needs to be TOLD, and it needs to be told
// ONCE — the event is the transition, not the state.
//
// The last verdict per agent is cached in memory (the aggregate itself is in SQLite, but
// "what did we last ANNOUNCE" is not a fact about jobs). A restart therefore re-arms the
// announcement: the first job of a still-degraded agent after a restart records
// agent.degraded again. That is the honest reading of "unknown → degraded" (the design's
// own wording) — the process genuinely no longer knows, and a missed page is worse than
// a repeated one.

import (
	"log/slog"

	"github.com/inhere/gofer/internal/agent"
)

// agentEventScopePrefix is the scope-id prefix of an agent's own event stream
// (`agent:<key>`): the health transitions belong to no job, and a subscriber filters on
// the agent. Mirrors planScopePrefix / xfer.EventJobID.
const agentEventScopePrefix = "agent:"

// AgentEventScope is the synthetic event-scope id of an agent (`agent:<key>`).
func AgentEventScope(agentKey string) string { return agentEventScopePrefix + agentKey }

// noteAgentHealth recomputes an agent's health verdict after one of its jobs reached a
// terminal state and records the transition, if any. It is called from finish() — the
// moment the provider's outcome is decided — and never from the review verdict: a human
// rejecting a delivery is not a provider failure (the aggregate itself counts a
// needs_review job as a success for exactly that reason).
//
// Best-effort throughout: health is an observation, and a job must never fail because an
// observation could not be written.
func (s *Service) noteAgentHealth(snap JobResult) {
	if snap.Agent == "" {
		return
	}
	cfg := s.config()
	if cfg == nil {
		return
	}
	hc := cfg.EffectiveAgentHealth()
	h, err := s.meta.AgentHealth(snap.Agent, s.nowFn().Unix()-int64(hc.WindowSec))
	if err != nil {
		slog.Warn("agent health: aggregate", "agent", snap.Agent, "err", err)
		return
	}
	state := agent.HealthState(h, hc)
	prev, known := s.swapAgentHealthState(snap.Agent, state)
	if known && prev == state {
		return
	}
	// The failing run's own words: its error when it has one, and otherwise its status
	// (a non-zero exit with no message is the common case for a provider that just dies
	// — "failed" is the honest description of that).
	lastErr := firstRunes(snap.Error, maxTodoNoteErrorRunes)
	if lastErr == "" {
		lastErr = snap.Status
	}
	switch {
	case state == agent.HealthDegraded && prev != agent.HealthDegraded:
		s.RecordScopedEvent(AgentEventScope(snap.Agent), EventAgentDegraded, snap.ProjectKey, map[string]any{
			"agent": snap.Agent, "transient_fail": h.TransientFail, "window_sec": hc.WindowSec,
			// The failure that took it over the threshold — the evidence a reader acts on.
			"last_error": lastErr,
		})
	case state == agent.HealthHealthy && prev == agent.HealthDegraded:
		s.RecordScopedEvent(AgentEventScope(snap.Agent), EventAgentRecovered, snap.ProjectKey, map[string]any{
			"agent": snap.Agent, "window_sec": hc.WindowSec,
		})
	}
}

// swapAgentHealthState stores the verdict just computed and returns the one it replaced
// (known=false when this agent has not been observed in this process before).
func (s *Service) swapAgentHealthState(agentKey, state string) (string, bool) {
	s.agentHealthMu.Lock()
	defer s.agentHealthMu.Unlock()
	if s.agentHealthState == nil {
		s.agentHealthState = make(map[string]string)
	}
	prev, known := s.agentHealthState[agentKey]
	s.agentHealthState[agentKey] = state
	return prev, known
}
