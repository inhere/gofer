package job

import (
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// T7 (design 2026-11-03 关键流程: 父 job 结束): every way a parent job goes away — a plain
// terminal outcome, a takeover, a park in needs_review and a delivery check whose
// continuation could not be submitted — retires the receipts registered on it and reports
// each one to a person exactly once. The parent is never resumed.

// childWakeParentCfg is the T7 service config: the "gated" agent (a plain success) and the
// "codex" agent (a gated provider error that is taken over).
func childWakeParentCfg(gate childWakeGate, root, bin string) *config.Config {
	projects, runners := childWakeProjectCfg(root)
	return &config.Config{
		Storage:  config.StorageConfig{Root: root},
		Projects: projects,
		Runners:  runners,
		Agents:   childWakeAgents(bin, gate),
	}
}

// gatedParentReq is a job on a gated agent; SessionID makes the "codex" one eligible for
// an automatic continuation.
func gatedParentReq(agentKey string) JobRequest {
	return JobRequest{
		ProjectKey: "self", Agent: agentKey, Runner: "local",
		Prompt: "supervise the children", Cwd: ".", TimeoutSec: 30, SessionID: "sess-parent",
	}
}

// parentEndReceipts registers one automatic and one parked manual receipt on parentID and
// returns them. The manual one stands for a hand-registered "tell me when the child is
// done" that was waiting for delivery.
func parentEndReceipts(t *testing.T, s *Service, parentID, childID string) (auto, manual jobstore.WakeupRecord) {
	t.Helper()
	addChildJob(t, s, childID, StatusDone, 0, "a child title")
	auto = addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parentID, FilterJobID: childID, AutoChild: 1})
	manual = addChildReceipt(t, s, jobstore.WakeupRecord{JobID: parentID, FilterJobID: childID})
	if err := s.meta.MarkWakeupPendingSay(manual.ID); err != nil {
		t.Fatalf("park the manual receipt: %v", err)
	}
	return auto, manual
}

// assertParentEndReported checks the single, final report of a parent that went away: one
// job.child_wake_failed per automatic receipt (machine reason, no child title), one
// job.wakeup_failed for the parked manual one, and every receipt disarmed.
func assertParentEndReported(t *testing.T, s *Service, parentID string, auto, manual jobstore.WakeupRecord) {
	t.Helper()
	types := eventTypes(t, s, parentID)
	assert.Eq(t, 1, countEvents(types, EventJobChildWakeFailed), "automatic receipts reported")
	assert.Eq(t, 1, countEvents(types, EventJobWakeupFailed), "parked manual receipts reported")
	for _, id := range []string{auto.ID, manual.ID} {
		if got := childWakeRow(t, s, id); got.Enabled != 0 {
			t.Fatalf("wakeup %s stayed armed after its parent ended", id)
		}
	}
	events, err := s.ListJobEvents(parentID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents(%s): %v", parentID, err)
	}
	for _, ev := range events {
		if ev.Type != EventJobChildWakeFailed {
			continue
		}
		assert.True(t, strings.Contains(ev.Detail, "child_job_id"), "detail = %q", ev.Detail)
		assert.False(t, strings.Contains(ev.Detail, "a child title"), "detail carried the child title: %q", ev.Detail)
		assert.True(t, strings.Contains(ev.Detail, `"reason":"`+childWakeReasonParentEnded+`"`),
			"detail = %q, want the machine reason %q", ev.Detail, childWakeReasonParentEnded)
	}
}

// assertParentNotResumed checks the receipts did not start a continuation of the parent:
// the job ended as itself and nothing new was submitted for it.
func assertParentNotResumed(t *testing.T, s *Service, final JobResult) {
	t.Helper()
	assert.Eq(t, "", final.AutoResumedBy, "the ended parent was continued")
	assert.Eq(t, "", final.FellBackTo, "the ended parent was transferred")
	if sayRows, err := s.meta.ListPendingSay(); err == nil {
		for _, w := range sayRows {
			if w.JobID == final.ID && w.AutoChild == 1 {
				t.Fatalf("a receipt of the ended parent %s is still waiting for delivery", final.ID)
			}
		}
	}
}

// TestChildWakeParentEndTerminal: a parent that ends normally retires its receipts and
// reports each one once.
func TestChildWakeParentEndTerminal(t *testing.T) {
	t.Parallel()
	root, gate := t.TempDir(), newChildWakeGate(t)
	s, _ := newChildWakeService(t, childWakeParentCfg(gate, root, testcmd.Path(t)), root)

	parent := mustSubmit(t, s, gatedParentReq("gated"))
	auto, manual := parentEndReceipts(t, s, parent.ID, "child-end-t")
	gate.Release(t)
	final := settled(t, s, parent.ID)
	assert.Eq(t, StatusDone, final.Status)

	assertParentEndReported(t, s, final.ID, auto, manual)
	assertParentNotResumed(t, s, final)
	// A manual receipt that was never delivered is not counted as one that fired.
	assert.Eq(t, int64(0), childWakeRow(t, s, manual.ID).FiredCount)
}

// TestChildWakeParentEndTakeover: a parent whose work is TAKEN OVER (auto-resumed into a
// new job) has still ended as a session: its receipts are reported, never left watching a
// parent id nothing will speak to again.
func TestChildWakeParentEndTakeover(t *testing.T) {
	t.Parallel()
	root, gate := t.TempDir(), newChildWakeGate(t)
	s, _ := newChildWakeService(t, childWakeParentCfg(gate, root, testcmd.Path(t)), root)

	parent := mustSubmit(t, s, gatedParentReq("codex"))
	auto, manual := parentEndReceipts(t, s, parent.ID, "child-end-takeover")
	gate.Release(t)

	waitForJobEvents(t, s, parent.ID, EventJobChildWakeFailed)
	src := waitAutoResumed(t, s, parent.ID, true)
	assertParentEndReported(t, s, src.ID, auto, manual)
	// The takeover is the AGENT's continuation, not a receipt-driven one: the ended parent
	// keeps its own id and the receipts reported, they did not resume anything.
	assert.Eq(t, 1, countEvents(eventTypes(t, s, src.ID), EventJobChildWakeFailed))
}

// TestChildWakeParentEndNeedsReview: a parent parked for review has ENDED (its process is
// gone), so its receipts are reported at the park — and a later verdict on that park does
// not report them a second time.
func TestChildWakeParentEndNeedsReview(t *testing.T) {
	t.Parallel()
	root, gate := t.TempDir(), newChildWakeGate(t)
	s, _ := newChildWakeService(t, childWakeParentCfg(gate, root, testcmd.Path(t)), root)

	req := gatedParentReq("gated")
	req.Review = true
	parent := mustSubmit(t, s, req)
	auto, manual := parentEndReceipts(t, s, parent.ID, "child-end-review")
	gate.Release(t)
	final := settled(t, s, parent.ID)
	assert.Eq(t, StatusNeedsReview, final.Status)

	assertParentEndReported(t, s, final.ID, auto, manual)

	// A human accepts the delivery: the job re-records job.terminal, which must not report
	// the same receipts again (they are already disarmed).
	if _, err := s.AcceptJob(final.ID, "reviewer", ""); err != nil {
		t.Fatalf("AcceptJob(%s): %v", final.ID, err)
	}
	types := eventTypes(t, s, final.ID)
	assert.Eq(t, 1, countEvents(types, EventJobChildWakeFailed), "the verdict reported the receipts again")
	assert.Eq(t, 1, countEvents(types, EventJobWakeupFailed))
	assert.False(t, contains(types, EventJobWakeupFired), "a receipt fired on the parent's verdict: %v", types)
}

// TestChildWakeParentEndFailedCheckResume: a delivery check whose continuation could not be
// submitted parks the job in needs_review through parkAfterFailedCheckResume — the fourth
// ending that records no job.terminal and therefore needs its own report.
func TestChildWakeParentEndFailedCheckResume(t *testing.T) {
	t.Parallel()
	root, gate := t.TempDir(), newChildWakeGate(t)
	bin := testcmd.Path(t)
	projects, runners := childWakeProjectCfg(root)
	cfg := &config.Config{
		Server:   config.ServerConfig{DeliveryCheck: enabledCheck(2)},
		Storage:  config.StorageConfig{Root: root},
		Projects: projects,
		Runners:  runners,
		Agents:   childWakeAgents(bin, gate),
	}
	s, _ := newChildWakeService(t, cfg, root)
	checker := &fakeChecker{outs: []fakeCheckAnswer{{out: checkFailJSON}}}
	checker.onCall = func() {
		// The continuation is submitted after the checker answered: drop the project so
		// that submission is refused (the parkAfterFailedCheckResume path).
		broken := s.config().Clone()
		broken.Projects["self"] = config.ProjectConfig{HostPath: broken.Projects["self"].HostPath,
			AllowedAgents: []string{"codex"}, AllowedRunners: []string{"nowhere"}}
		s.Reload(broken)
	}
	s.SetDeliveryChecker(checker)

	req := gatedParentReq("gated")
	req.CheckPrompt = "at least two commits"
	parent := mustSubmit(t, s, req)
	auto, manual := parentEndReceipts(t, s, parent.ID, "child-end-check")
	gate.Release(t)
	final := settled(t, s, parent.ID)
	if final.Status != StatusNeedsReview || !strings.Contains(final.CheckError, "resume not submitted") {
		t.Fatalf("status=%s check_error=%q, want the parked-after-failed-check ending", final.Status, final.CheckError)
	}
	assert.False(t, contains(eventTypes(t, s, final.ID), EventJobTerminal),
		"a delivery-check park must not record job.terminal")
	assert.Eq(t, 1, checker.count(), "the delivery check ran once")

	assertParentEndReported(t, s, final.ID, auto, manual)
}
