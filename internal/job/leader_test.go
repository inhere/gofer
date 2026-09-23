package job

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/notify"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// MCP-05 阶段 B (S3) tests — the leader round. The fixture mirrors newChainService: a
// cli-agent that echoes its argv (so the leader job's PROMPT is observable in its
// stdout/request) for the members and a second one for the leader, plus a
// supervisor.leader block the subtests tune.

// newLeaderService builds the leader fixture: agents "ok" (echoes, done), "boom"
// (exit 1, failed) and "lead" (the leader), the project "self" allowing all three, and
// `supervisor.leader` enabled with a LONG wake delay — every test fires the wake
// explicitly through SweepDueLeaderWakes so no round depends on wall-clock timing.
func newLeaderService(t *testing.T, root string, mutate func(*config.Config)) *Service {
	t.Helper()
	bin := testcmd.Path(t)
	agentOf := func(args ...string) config.AgentConfig {
		return config.AgentConfig{Type: agent.TypeCLIAgent, Command: bin, Args: args}
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"ok", "boom", "lead"},
				AllowedRunners: []string{"local"},
			},
		},
		Agents: map[string]config.AgentConfig{
			"ok":   agentOf("argv", "{{prompt}}"),
			"boom": agentOf("exit", "1"),
			"lead": agentOf("argv", "{{prompt}}"),
		},
		Supervisor: &config.SupervisorConfig{Leader: &config.LeaderConfig{
			Enabled:           true,
			Agent:             "lead",
			Scopes:            []string{"plan"},
			MaxRoundsPerScope: 6,
			OnMemberDone:      boolPtr(true),
			WakeDelaySec:      3600,
		}},
	}
	if mutate != nil {
		mutate(cfg)
	}
	return newServiceFromCfg(t, root, cfg)
}

// seedLeaderPlan inserts a flat plan with one item per given title (a flat checklist
// keeps PLAN-03's chain-out behaviours out of the way).
func seedLeaderPlan(t *testing.T, s *Service, planID string, titles ...string) {
	t.Helper()
	if err := s.Meta().InsertPlan(jobstore.Plan{
		PlanID: planID, Title: "计划 " + planID, Description: "把这件事做完",
		Status: jobstore.PlanOpen, ProjectKey: "self", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("insert plan: %v", err)
	}
	for i, title := range titles {
		if err := s.Meta().InsertTodo(jobstore.PlanTodo{
			TodoID: planID + "-t" + title, PlanID: planID, Title: title,
			Status: jobstore.TodoPending, Sort: (i + 1) * 10, CreatedAt: 1, UpdatedAt: 1,
		}); err != nil {
			t.Fatalf("insert todo: %v", err)
		}
	}
}

// leaderMemberJob runs one member job of the plan to its FINISHED state. status picks
// the ending the wake rules care about: done (echo), failed (exit 1) or needs_review
// (a delivery parked for a human).
func leaderMemberJob(t *testing.T, s *Service, planID, marker, status string) JobResult {
	t.Helper()
	req := JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local",
		PlanID: planID, Title: "成员 " + marker,
		Prompt: "MEMBER-REPORT-" + marker, TimeoutSec: 30,
	}
	if status == StatusFailed {
		req.Agent = "boom"
	}
	if status == StatusNeedsReview {
		req.Review = true
	}
	res := submitAndWait(t, s, req)
	if res.Status != status {
		t.Fatalf("member job %s status = %s, want %s", res.ID, res.Status, status)
	}
	return res
}

// leaderWakes lists a plan's wake rows (the durable "待唤醒" records).
func leaderWakes(t *testing.T, s *Service, planID string) []jobstore.LeaderWake {
	t.Helper()
	ws, err := s.Meta().ListLeaderWakesForPlan(planID)
	if err != nil {
		t.Fatalf("ListLeaderWakesForPlan(%s): %v", planID, err)
	}
	return ws
}

// leaderJobPrompt reads the prompt the leader job was submitted with (request_json is
// the only place it is kept — the job row carries no prompt column).
func leaderJobPrompt(t *testing.T, s *Service, jobID string) string {
	t.Helper()
	rec, ok, err := s.Meta().GetJob(jobID)
	if err != nil || !ok {
		t.Fatalf("GetJob(%s): %v ok=%t", jobID, err, ok)
	}
	var req JobRequest
	if err := json.Unmarshal([]byte(rec.RequestJSON), &req); err != nil {
		t.Fatalf("decode request_json of %s: %v", jobID, err)
	}
	return req.Prompt
}

// TestLeaderWokenOnMemberTerminal is the core of 阶段 B: a member job of a plan
// reaching a FINISHED state (done / failed / needs_review) leaves a durable pending
// wake, the sweeper turns it into a leader job bound to the same plan — tagged
// leader + leader_of:<member>, carrying the plan, the checklist, the member's report
// tail and the action list — and the plan's event stream says so.
func TestLeaderWokenOnMemberTerminal(t *testing.T) {
	for _, status := range []string{StatusDone, StatusFailed, StatusNeedsReview} {
		t.Run(status, func(t *testing.T) {
			s := newLeaderService(t, t.TempDir(), nil)
			seedLeaderPlan(t, s, "plan-1", "先做这一步", "再做下一步")
			optInPlanLeader(t, s, "plan-1")
			member := leaderMemberJob(t, s, "plan-1", "77", status)

			// 1. the terminal left exactly one PENDING wake, due after wake_delay_sec.
			wakes := leaderWakes(t, s, "plan-1")
			if len(wakes) != 1 {
				t.Fatalf("wakes = %d rows, want 1 (%+v)", len(wakes), wakes)
			}
			w := wakes[0]
			if w.State != jobstore.LeaderWakePending {
				t.Fatalf("wake state = %s, want pending", w.State)
			}
			if w.MemberJobID != member.ID || w.MemberStatus != status {
				t.Fatalf("wake = %+v, want member %s/%s", w, member.ID, status)
			}
			if w.Round != 1 {
				t.Fatalf("round = %d, want 1 (the first leader round)", w.Round)
			}
			if w.DueAt <= w.CreatedAt {
				t.Fatalf("due_at %d is not after created_at %d (wake_delay_sec did not apply)", w.DueAt, w.CreatedAt)
			}

			// 2. the sweeper fires it once it is due — and only once.
			now := w.DueAt + 1
			n, err := s.SweepDueLeaderWakes(now)
			if err != nil {
				t.Fatalf("SweepDueLeaderWakes: %v", err)
			}
			if n != 1 {
				t.Fatalf("sweep started %d leader jobs, want 1", n)
			}
			if n2, _ := s.SweepDueLeaderWakes(now); n2 != 0 {
				t.Fatalf("a second sweep started %d more leader jobs, want 0", n2)
			}
			fired := leaderWakes(t, s, "plan-1")[0]
			if fired.State != jobstore.LeaderWakeFired || fired.LeaderJobID == "" {
				t.Fatalf("wake after the sweep = %+v, want state=fired with a leader job", fired)
			}

			// 3. the leader job is a normal job bound to the plan and marked as the leader.
			lead, ok := s.Get(fired.LeaderJobID)
			if !ok {
				t.Fatalf("leader job %s not found", fired.LeaderJobID)
			}
			if lead.Agent != "lead" || lead.PlanID != "plan-1" {
				t.Fatalf("leader job = agent %q plan %q, want lead/plan-1", lead.Agent, lead.PlanID)
			}
			if !hasTag(lead.Tags, leaderTag) || !hasTag(lead.Tags, leaderOfTag+member.ID) {
				t.Fatalf("leader job tags = %v, want %s and %s%s", lead.Tags, leaderTag, leaderOfTag, member.ID)
			}
			rec, _, err := s.Meta().GetJob(lead.ID)
			if err != nil {
				t.Fatalf("GetJob: %v", err)
			}
			if rec.LeaderOfPlan != "plan-1" {
				t.Fatalf("row leader_of_plan = %q, want plan-1 (the marker must survive a restart)", rec.LeaderOfPlan)
			}
			if memberRec, _, _ := s.Meta().GetJob(member.ID); memberRec.LeaderOfPlan != "" {
				t.Fatalf("the MEMBER job carries the leader marker: %q", memberRec.LeaderOfPlan)
			}

			// 4. the prompt is the decision brief: plan, checklist, report tail, actions.
			prompt := leaderJobPrompt(t, s, lead.ID)
			wants := []string{
				"计划 plan-1", "把这件事做完", // plan title + goal
				"先做这一步", "再做下一步", // the todo chain
				member.ID, "状态：" + status, // the member and how it ended
				"gofer plan comment", "gofer plan set-todo", "gofer plan ask", // what it may do (CLI, LEAD-02)
				"不能", "accept", // and what it may not
			}
			if status != StatusFailed {
				// A failed member (boom exits without printing) has no report tail —
				// the context block simply carries no "汇报" section.
				wants = append(wants, "MEMBER-REPORT-77")
			}
			for _, want := range wants {
				if !strings.Contains(prompt, want) {
					t.Fatalf("leader prompt is missing %q:\n%s", want, prompt)
				}
			}

			// 5. the plan's own event stream carries the wake.
			if events := commentEvents(t, s, PlanEventScope("plan-1")); !hasEvent(events, EventPlanLeaderWoken) {
				t.Fatalf("plan events = %v, want %s", events, EventPlanLeaderWoken)
			}
		})
	}
}

// TestLeaderDisabledByDefault was the pre-LEAD-02 "opt-in default" test. The default
// moved from one global switch to a per-plan one, so its two cases are now pinned by
// the tests that own each gate: TestLeaderOffByDefaultPerPlan (master switch on, plan
// off) and TestLeaderGlobalSwitchStillGates (plan on, master switch off). Keeping a
// third copy here would only re-test whichever gate happened to short-circuit first.

// TestLeaderJobDoesNotWakeLeader: the leader's own job reaching a terminal state must
// not start another round (防自激), and neither may any job that merely carries the
// `leader` tag — the tag is the documented "this is a leader run" mark a human/other
// tooling can also apply.
func TestLeaderJobDoesNotWakeLeader(t *testing.T) {
	t.Run("the leader job itself", func(t *testing.T) {
		s := newLeaderService(t, t.TempDir(), nil)
		seedLeaderPlan(t, s, "plan-1", "一步")
		optInPlanLeader(t, s, "plan-1")
		member := leaderMemberJob(t, s, "plan-1", "77", StatusDone)
		now := leaderWakes(t, s, "plan-1")[0].DueAt + 1
		if n, err := s.SweepDueLeaderWakes(now); err != nil || n != 1 {
			t.Fatalf("sweep = %d/%v, want one leader job", n, err)
		}
		leadID := leaderWakes(t, s, "plan-1")[0].LeaderJobID

		// Let the leader job finish, then sweep again: its terminal must leave nothing.
		if _, ok := s.Wait(leadID); !ok {
			t.Fatalf("leader job %s not found", leadID)
		}
		if wakes := leaderWakes(t, s, "plan-1"); len(wakes) != 1 {
			t.Fatalf("wakes after the leader finished = %+v, want only the fired one", wakes)
		}
		if n, _ := s.SweepDueLeaderWakes(now + 86400); n != 0 {
			t.Fatalf("the leader job's own terminal started %d more leader jobs", n)
		}
		_ = member
	})

	t.Run("a leader-tagged job", func(t *testing.T) {
		s := newLeaderService(t, t.TempDir(), nil)
		seedLeaderPlan(t, s, "plan-1", "一步")
		optInPlanLeader(t, s, "plan-1")
		submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "ok", Runner: "local", PlanID: "plan-1",
			Title: "带 leader 标记的普通 job", Tags: []string{leaderTag},
			Prompt: "x", TimeoutSec: 30,
		})
		if wakes := leaderWakes(t, s, "plan-1"); len(wakes) != 0 {
			t.Fatalf("a leader-tagged job woke a leader: %+v", wakes)
		}
	})
}

// TestLeaderRoundsCapped: once a plan has spent max_rounds_per_scope, a further member
// terminal does NOT write a wake — it records plan.leader_exhausted (in the
// notification default set, like plan.blocked) and raises a plan decision, the
// "待人处理" item a human answers on the plan page.
func TestLeaderRoundsCapped(t *testing.T) {
	s := newLeaderService(t, t.TempDir(), func(c *config.Config) {
		c.Supervisor.Leader.MaxRoundsPerScope = 1
	})
	seedLeaderPlan(t, s, "plan-1", "第一步", "第二步")
	optInPlanLeader(t, s, "plan-1")

	leaderMemberJob(t, s, "plan-1", "1", StatusDone)
	now := leaderWakes(t, s, "plan-1")[0].DueAt + 1
	if n, err := s.SweepDueLeaderWakes(now); err != nil || n != 1 {
		t.Fatalf("first sweep = %d/%v, want one leader job", n, err)
	}

	// Round 2 is over budget: no wake, an exhausted event, and something for a human.
	leaderMemberJob(t, s, "plan-1", "2", StatusDone)
	wakes := leaderWakes(t, s, "plan-1")
	if len(wakes) != 1 {
		t.Fatalf("wakes = %+v, want only the spent round", wakes)
	}
	if n, _ := s.SweepDueLeaderWakes(now + 86400); n != 0 {
		t.Fatalf("the exhausted plan still started %d leader jobs", n)
	}
	events := commentEvents(t, s, PlanEventScope("plan-1"))
	if !hasEvent(events, EventPlanLeaderExhausted) {
		t.Fatalf("plan events = %v, want %s", events, EventPlanLeaderExhausted)
	}
	decisions, err := s.Meta().ListDecisions("", "plan-1")
	if err != nil {
		t.Fatalf("ListDecisions: %v", err)
	}
	if len(decisions) == 0 {
		t.Fatal("leader exhausted raised no plan decision for a human to answer")
	}
	if decisions[0].State != jobstore.DecisionOpen {
		t.Fatalf("decision state = %s, want open (a human must still act)", decisions[0].State)
	}
	// The default notification set carries the signal (design §二.B.3).
	found := false
	for _, ev := range notify.DefaultTriggerEvents {
		if ev == EventPlanLeaderExhausted {
			found = true
		}
	}
	if !found {
		t.Fatalf("notify.DefaultTriggerEvents = %v, want it to include %s", notify.DefaultTriggerEvents, EventPlanLeaderExhausted)
	}
}

// TestLeaderSkippedWhenPlanPaused: a paused plan is a "一键叫停" — no leader round is
// started for a member that finished while it was held, and the plan's stream says why.
func TestLeaderSkippedWhenPlanPaused(t *testing.T) {
	s := newLeaderService(t, t.TempDir(), nil)
	seedLeaderPlan(t, s, "plan-1", "一步")
	optInPlanLeader(t, s, "plan-1")
	if err := s.Meta().SetPlanPaused("plan-1", true); err != nil {
		t.Fatalf("SetPlanPaused: %v", err)
	}
	leaderMemberJob(t, s, "plan-1", "77", StatusDone)

	if wakes := leaderWakes(t, s, "plan-1"); len(wakes) != 0 {
		t.Fatalf("a paused plan woke a leader: %+v", wakes)
	}
	detail := commentEventDetail(t, s, PlanEventScope("plan-1"), EventPlanLeaderSkipped)
	if !strings.Contains(detail, `"reason":"paused"`) {
		t.Fatalf("plan.leader_skipped detail = %q, want reason=paused", detail)
	}
	if n, _ := s.SweepDueLeaderWakes(time.Now().Unix() + 86400); n != 0 {
		t.Fatalf("sweep started %d leader jobs on a paused plan", n)
	}
}

// TestHumanCommentCancelsLeaderWake: the wake delay is the window a human gets to take
// over. A user's comment on the plan (or on one of its todos/jobs) cancels the pending
// round — the leader never runs — and the plan's stream records who intervened.
func TestHumanCommentCancelsLeaderWake(t *testing.T) {
	s := newLeaderService(t, t.TempDir(), nil)
	seedLeaderPlan(t, s, "plan-1", "一步")
	optInPlanLeader(t, s, "plan-1")
	member := leaderMemberJob(t, s, "plan-1", "77", StatusDone)
	due := leaderWakes(t, s, "plan-1")[0].DueAt

	if _, dispatched, err := s.Comment(jobstore.CommentScopePlan, "plan-1", "alice", jobstore.CommentAuthorUser, "这轮我来，别叫 leader 了"); err != nil {
		t.Fatalf("Comment: %v", err)
	} else if len(dispatched) != 0 {
		t.Fatalf("the comment dispatched %+v, want none (no mention)", dispatched)
	}

	wakes := leaderWakes(t, s, "plan-1")
	if len(wakes) != 1 || wakes[0].State != jobstore.LeaderWakeCancelled {
		t.Fatalf("wake after the human comment = %+v, want state=cancelled", wakes)
	}
	if wakes[0].CancelledBy != "alice" {
		t.Fatalf("cancelled_by = %q, want alice", wakes[0].CancelledBy)
	}
	if detail := commentEventDetail(t, s, PlanEventScope("plan-1"), EventPlanLeaderCancelled); !strings.Contains(detail, "alice") {
		t.Fatalf("plan.leader_cancelled detail = %q, want the author", detail)
	}
	if n, _ := s.SweepDueLeaderWakes(due + 86400); n != 0 {
		t.Fatalf("a cancelled wake still started %d leader jobs", n)
	}

	// A user comment on the MEMBER job (or its todo) cancels the plan's round too:
	// the human spoke about this chain, whichever thread they used.
	member2 := leaderMemberJob(t, s, "plan-1", "78", StatusDone)
	_ = member2
	if _, _, err := s.Comment(jobstore.CommentScopeJob, member2.ID, "alice", jobstore.CommentAuthorUser, "我看到你的汇报了"); err != nil {
		t.Fatalf("Comment(job): %v", err)
	}
	last := leaderWakes(t, s, "plan-1")[len(leaderWakes(t, s, "plan-1"))-1]
	if last.State != jobstore.LeaderWakeCancelled {
		t.Fatalf("the job-thread comment left the wake %s, want cancelled", last.State)
	}
	_ = member
}

// TestLeaderCommentCanDispatch: 阶段 B's whitelist opens exactly one door in the
// 阶段 A gate — a comment written by a LEADER JOB of this plan may @-mention and
// dispatch. A member job's comment on the same thread is still recorded only.
func TestLeaderCommentCanDispatch(t *testing.T) {
	s := newLeaderService(t, t.TempDir(), nil)
	seedLeaderPlan(t, s, "plan-1", "一步")
	optInPlanLeader(t, s, "plan-1")
	member := leaderMemberJob(t, s, "plan-1", "77", StatusDone)
	now := leaderWakes(t, s, "plan-1")[0].DueAt + 1
	if n, err := s.SweepDueLeaderWakes(now); err != nil || n != 1 {
		t.Fatalf("sweep = %d/%v, want one leader job", n, err)
	}
	leadID := leaderWakes(t, s, "plan-1")[0].LeaderJobID

	cm, dispatched, err := s.CommentAsJob(jobstore.CommentScopePlan, "plan-1", leadID, "@ok 接着把第二件事做了")
	if err != nil {
		t.Fatalf("CommentAsJob(leader): %v", err)
	}
	if len(dispatched) != 1 {
		t.Fatalf("the leader's mention dispatched %+v, want one job", dispatched)
	}
	if cm.AuthorKind != jobstore.CommentAuthorAgent || cm.Author != "lead" {
		t.Fatalf("leader comment author = %s/%s, want lead/agent", cm.Author, cm.AuthorKind)
	}
	if events := commentEvents(t, s, PlanEventScope("plan-1")); !hasEvent(events, EventCommentTriggered) {
		t.Fatalf("plan events = %v, want %s", events, EventCommentTriggered)
	}

	// A MEMBER job speaks on its own plan's thread: recorded, nothing dispatched.
	// (Its own plan, so the throttle of the plan above cannot mask the reason.)
	seedLeaderPlan(t, s, "plan-2", "一步")
	member2 := leaderMemberJob(t, s, "plan-2", "88", StatusDone)
	if _, d, err := s.CommentAsJob(jobstore.CommentScopePlan, "plan-2", member2.ID, "@ok 你也来做一件"); err != nil {
		t.Fatalf("CommentAsJob(member): %v", err)
	} else if len(d) != 0 {
		t.Fatalf("a MEMBER job's mention dispatched %+v, want none (阶段 A gate)", d)
	}
	if events := commentEvents(t, s, PlanEventScope("plan-2")); hasEvent(events, EventCommentTriggered) {
		t.Fatalf("plan-2 events = %v, want no %s for a member's comment", events, EventCommentTriggered)
	}
	_ = member
}

// TestLeaderSkippedWhenPlanDone (S4, 2026-09-23): a plan whose items are ALL finished
// is `done`, and a leader round on it would have nothing to decide (its brief is built
// from the checklist). The round is not armed, and the plan's stream says why — the S3
// record had deliberately left this case open ("若运行中发现这是纯噪音").
func TestLeaderSkippedWhenPlanDone(t *testing.T) {
	s := newLeaderService(t, t.TempDir(), nil)
	seedLeaderPlan(t, s, "plan-1", "一步")
	optInPlanLeader(t, s, "plan-1")
	if err := s.Meta().SetPlanStatus("plan-1", jobstore.PlanDone, 100); err != nil {
		t.Fatalf("SetPlanStatus: %v", err)
	}
	leaderMemberJob(t, s, "plan-1", "88", StatusDone)

	if wakes := leaderWakes(t, s, "plan-1"); len(wakes) != 0 {
		t.Fatalf("a done plan woke a leader: %+v", wakes)
	}
	detail := commentEventDetail(t, s, PlanEventScope("plan-1"), EventPlanLeaderSkipped)
	if !strings.Contains(detail, `"reason":"plan_done"`) {
		t.Fatalf("plan.leader_skipped detail = %q, want reason=plan_done", detail)
	}
	if n, _ := s.SweepDueLeaderWakes(time.Now().Unix() + 86400); n != 0 {
		t.Fatalf("sweep started %d leader jobs on a done plan", n)
	}

	// The REAL completion path, not just a hand-set status: a member job bound to the
	// plan's last todo finishing makes linkTodoOutcome mark the todo done and advancePlan
	// complete the plan BEFORE the wake hook reads it — so the last member of a chain
	// never summons a round on a chain that is already over.
	s2 := newLeaderService(t, t.TempDir(), nil)
	seedLeaderPlan(t, s2, "plan-2", "唯一一步")
	optInPlanLeader(t, s2, "plan-2")
	res := submitAndWait(t, s2, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local",
		PlanID: "plan-2", TodoID: "plan-2-t唯一一步", Title: "成员 2",
		Prompt: "MEMBER-REPORT-2", TimeoutSec: 30,
	})
	if res.Status != StatusDone {
		t.Fatalf("member job status = %s, want done", res.Status)
	}
	plan, ok, err := s2.Meta().GetPlan("plan-2")
	if err != nil || !ok {
		t.Fatalf("GetPlan(plan-2): %v ok=%t", err, ok)
	}
	if plan.Status != jobstore.PlanDone {
		t.Fatalf("plan-2 status = %s, want done once its only todo finished", plan.Status)
	}
	if wakes := leaderWakes(t, s2, "plan-2"); len(wakes) != 0 {
		t.Fatalf("completing the chain woke a leader: %+v", wakes)
	}
	if detail := commentEventDetail(t, s2, PlanEventScope("plan-2"), EventPlanLeaderSkipped); !strings.Contains(detail, `"reason":"plan_done"`) {
		t.Fatalf("plan.leader_skipped detail = %q, want reason=plan_done", detail)
	}
}
