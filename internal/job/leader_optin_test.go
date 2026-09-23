package job

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// LEAD-02 (C2) tests: the leader round is opted into PER PLAN. The global
// supervisor.leader block still carries the agent/delay/rounds and keeps `enabled` as
// the master switch, but a plan that wants rounds must say so itself (`leader: on`) —
// a 真机验收 on v0.57 showed that one global switch woke the leader for EVERY open
// plan on the machine.

// optInPlanLeader flips a seeded plan's leader switch on (`leader: on`).
func optInPlanLeader(t *testing.T, s *Service, planID string) {
	t.Helper()
	if err := s.Meta().SetPlanLeader(planID, jobstore.PlanLeaderOn); err != nil {
		t.Fatalf("SetPlanLeader(%s): %v", planID, err)
	}
}

// leaderEvents returns the plan scope's leader events as `type|detail` lines, so a
// test can assert both what was recorded and why.
func leaderEvents(t *testing.T, s *Service, planID string) []string {
	t.Helper()
	evs, err := s.ListJobEvents(PlanEventScope(planID), 0)
	if err != nil {
		t.Fatalf("ListJobEvents(%s): %v", PlanEventScope(planID), err)
	}
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		if strings.HasPrefix(e.Type, "plan.leader_") {
			out = append(out, e.Type+"|"+e.Detail)
		}
	}
	return out
}

// leaderSkipReason reads the `reason` field out of a plan.leader_skipped detail_json.
func leaderSkipReason(t *testing.T, detail string) string {
	t.Helper()
	var d struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(detail), &d); err != nil {
		t.Fatalf("decode plan.leader_skipped detail %q: %v", detail, err)
	}
	return d.Reason
}

// TestLeaderOffByDefaultPerPlan: the master switch is ON, the plan never opted in — a
// member job reaching a FINISHED state wakes nobody, and (T2.3) the plan's stream stays
// SILENT: a skip line per member terminal on every plan nobody opted in would be pure
// noise.
func TestLeaderOffByDefaultPerPlan(t *testing.T) {
	s := newLeaderService(t, t.TempDir(), nil)
	seedLeaderPlan(t, s, "plan-1", "一步")
	_ = leaderMemberJob(t, s, "plan-1", "77", StatusDone)

	if wakes := leaderWakes(t, s, "plan-1"); len(wakes) != 0 {
		t.Fatalf("wakes = %+v, want none for a plan that did not opt in", wakes)
	}
	if n, err := s.SweepDueLeaderWakes(time.Now().Unix() + 86400); err != nil || n != 0 {
		t.Fatalf("sweep = %d/%v, want no leader job on an opted-out plan", n, err)
	}
	if evs := leaderEvents(t, s, "plan-1"); len(evs) != 0 {
		t.Fatalf("plan events = %v, want none (an un-opted-in plan must not log a skip per member)", evs)
	}
}

// TestLeaderOnlyForOptedInPlan: two open plans of the same server, only P1 opted in.
// The member terminal of either plan arms a round; only P1's fires.
func TestLeaderOnlyForOptedInPlan(t *testing.T) {
	s := newLeaderService(t, t.TempDir(), nil)
	seedLeaderPlan(t, s, "plan-1", "一步")
	seedLeaderPlan(t, s, "plan-2", "一步")
	optInPlanLeader(t, s, "plan-1")

	opted := leaderMemberJob(t, s, "plan-1", "1", StatusDone)
	_ = leaderMemberJob(t, s, "plan-2", "2", StatusDone)

	if wakes := leaderWakes(t, s, "plan-2"); len(wakes) != 0 {
		t.Fatalf("the opted-out plan armed %+v, want none", wakes)
	}
	wakes := leaderWakes(t, s, "plan-1")
	if len(wakes) != 1 || wakes[0].MemberJobID != opted.ID {
		t.Fatalf("wakes = %+v, want exactly the member %s of plan-1", wakes, opted.ID)
	}

	now := wakes[0].DueAt + 1
	n, err := s.SweepDueLeaderWakes(now)
	if err != nil || n != 1 {
		t.Fatalf("sweep = %d/%v, want one leader job", n, err)
	}
	lead := leaderWakes(t, s, "plan-1")[0]
	if lead.State != jobstore.LeaderWakeFired {
		t.Fatalf("wake after the sweep = %+v, want fired", lead)
	}
	job, ok := s.Get(lead.LeaderJobID)
	if !ok || job.PlanID != "plan-1" {
		t.Fatalf("leader job %s = %+v (ok=%t), want it bound to plan-1", lead.LeaderJobID, job, ok)
	}
	// The plan that did NOT opt in still has nothing — not even after a later sweep.
	if n2, _ := s.SweepDueLeaderWakes(now + 86400); n2 != 0 {
		t.Fatalf("a later sweep started %d leader jobs for the opted-out plan", n2)
	}
	if wakes := leaderWakes(t, s, "plan-2"); len(wakes) != 0 {
		t.Fatalf("plan-2 wakes = %+v, want none", wakes)
	}
}

// TestLeaderGlobalSwitchStillGates: the plan is switched on but the master switch is
// off — nothing is armed, and the plan's stream says the round was skipped for that
// reason (the design keeps `enabled` as the 总闸).
func TestLeaderGlobalSwitchStillGates(t *testing.T) {
	s := newLeaderService(t, t.TempDir(), func(c *config.Config) {
		c.Supervisor.Leader.Enabled = false
	})
	seedLeaderPlan(t, s, "plan-1", "一步")
	optInPlanLeader(t, s, "plan-1")
	_ = leaderMemberJob(t, s, "plan-1", "77", StatusDone)

	if wakes := leaderWakes(t, s, "plan-1"); len(wakes) != 0 {
		t.Fatalf("wakes = %+v, want none while the master switch is off", wakes)
	}
	// The reason is recorded where a human reading the plan can see it. (The "plan off"
	// case above is deliberately silent; this one is a switch somebody turned ON.)
	reason := leaderSkipReason(t, commentEventDetail(t, s, PlanEventScope("plan-1"), EventPlanLeaderSkipped))
	if reason != leaderSkipGlobalOff {
		t.Fatalf("plan.leader_skipped reason = %q, want %q", reason, leaderSkipGlobalOff)
	}
	if n, _ := s.SweepDueLeaderWakes(time.Now().Unix() + 86400); n != 0 {
		t.Fatalf("sweep started %d leader jobs with the master switch off", n)
	}
}

// TestLeaderPromptListsCliActions: the leader's action list is CLI commands, not MCP
// tool names — MCP is only reachable for agents that registered the gofer MCP server
// (v0.57 验收: a leader ran with NO MCP tools and fell back to the inherited server
// token). The prompt must also state that the credential enforces the limits.
func TestLeaderPromptListsCliActions(t *testing.T) {
	s := newLeaderService(t, t.TempDir(), nil)
	seedLeaderPlan(t, s, "plan-1", "一步", "二步")
	optInPlanLeader(t, s, "plan-1")
	_ = leaderMemberJob(t, s, "plan-1", "77", StatusDone)

	now := leaderWakes(t, s, "plan-1")[0].DueAt + 1
	if n, err := s.SweepDueLeaderWakes(now); err != nil || n != 1 {
		t.Fatalf("sweep = %d/%v, want one leader job", n, err)
	}
	prompt := leaderJobPrompt(t, s, leaderWakes(t, s, "plan-1")[0].LeaderJobID)

	for _, want := range []string{
		"gofer plan comment",                  // speak in the thread (and @-dispatch)
		"gofer plan set-todo",                 // move a checklist item
		"gofer plan set-todo <todo> --status", // the exact shape, ready|skipped spelled out
		"gofer job wakeup create",             // arm a wakeup
		"gofer plan ask",                      // escalate to a human
		"403",                                 // the limits are enforced by the credential
		"凭证",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("leader prompt is missing %q:\n%s", want, prompt)
		}
	}
	// No MCP tool name may survive: an agent without the gofer MCP server would read
	// them as "the only way to act" and go looking for the inherited server token again.
	for _, banned := range []string{
		"gofer_update_todo", "gofer_list_comments", "gofer_get_plan", "gofer_comment",
		"gofer_wakeup_create", "gofer_ask_human",
	} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("leader prompt still names the MCP tool %q:\n%s", banned, prompt)
		}
	}
}
