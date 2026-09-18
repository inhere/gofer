package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestAgentHealthAggregates: the health read behind `gofer agent status`, the web
// badge and the pre-dispatch decision aggregates the jobs table per agent inside the
// window — counting delivered work as a success (a job parked for human review was
// still delivered), separating provider errors from real failures, and reporting
// enough to tell "no evidence" from "healthy".
func TestAgentHealthAggregates(t *testing.T) {
	s := openTest(t)
	const now = int64(1_700_000_000)
	const since = now - 3600

	put := func(id, agent, status, class string, started, ended int64) {
		t.Helper()
		rec := sampleJob(id, "proj", started)
		rec.Agent, rec.Status, rec.FailureClass = agent, status, class
		rec.EndedAt, rec.UpdatedAt = ended, ended
		assert.NoErr(t, s.UpsertJob(rec))
	}

	// codex: a transient failure, then a success, then another transient failure with
	// nothing after it — recovered once, degraded again.
	put("cx-1", "codex", "failed", "transient", since+10, since+20)
	put("cx-2", "codex", "done", "", since+30, since+40)
	put("cx-3", "codex", "failed", "transient", since+50, since+60)
	// claude: a delivered-but-unreviewed job is a provider success; a real bug is not.
	put("cl-1", "claude", "needs_review", "", since+10, since+20)
	put("cl-2", "claude", "failed", "other", since+30, since+40)
	// omp: one job inside the window that has not ended, and one that predates it.
	put("om-run", "omp", "running", "", since+10, 0)
	put("om-old", "omp", "done", "", since-200, since-100)

	h, err := s.AgentHealth("codex", since)
	assert.NoErr(t, err)
	if h.Jobs != 3 || h.OK != 1 || h.TransientFail != 2 || h.OtherFail != 0 {
		t.Fatalf("codex = %+v, want 3 jobs / 1 ok / 2 transient / 0 other", h)
	}
	if h.LastTransientAt != since+60 || h.LastOKAt != since+40 {
		t.Fatalf("codex last outcomes = transient %d / ok %d, want %d / %d", h.LastTransientAt, h.LastOKAt, since+60, since+40)
	}
	// The recovery count is evidence AFTER the last transient failure: codex's single
	// success happened before its latest outage, so it does not count.
	if h.OKSinceTransient != 0 {
		t.Fatalf("ok_since_transient = %d, want 0 (the success predates the last outage)", h.OKSinceTransient)
	}

	all, err := s.AgentHealthAll(since)
	assert.NoErr(t, err)
	if len(all) != 3 {
		t.Fatalf("agents in the window = %d (%v), want codex/claude/omp", len(all), all)
	}
	// claude never hit a provider error, so there is no outage to recover from:
	// ok_since_transient is 0 by definition (it counts successes AFTER the last
	// transient failure, and there is none) — HealthState never needs it in that case.
	if got := all["claude"]; got.OK != 1 || got.OtherFail != 1 || got.TransientFail != 0 || got.OKSinceTransient != 0 {
		t.Fatalf("claude = %+v, want 1 ok (needs_review counts) / 1 other / 0 transient", got)
	}
	if got := all["omp"]; got.Jobs != 1 || got.OK != 0 {
		t.Fatalf("omp = %+v, want the in-window running job only", got)
	}
	if _, ok := all["ghost"]; ok {
		t.Fatal("an agent with no job in the window must not appear in the aggregate map")
	}

	// Recovery: a success AFTER the outage restores the agent.
	put("cx-4", "codex", "done", "", since+70, since+80)
	h, err = s.AgentHealth("codex", since)
	assert.NoErr(t, err)
	if h.OKSinceTransient != 1 || h.LastOKAt != since+80 {
		t.Fatalf("codex after a new success = %+v, want ok_since_transient 1 and last_ok_at %d", h, since+80)
	}

	// An agent the window holds no evidence for is a zero record, never an error.
	ghost, err := s.AgentHealth("ghost", since)
	assert.NoErr(t, err)
	if ghost.Jobs != 0 || ghost.Agent != "ghost" || ghost.TransientFail != 0 {
		t.Fatalf("ghost = %+v, want a zero record naming the agent", ghost)
	}
}
