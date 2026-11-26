package stage

import (
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

func waitingKinds(t *testing.T, f *fix, allow func(string) bool) map[string]string {
	t.Helper()
	facts, err := f.svc.Waiting(allow)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, w := range facts {
		out[w.WorkItemID] = w.Kind
	}
	return out
}

func TestWaitingReviewFactAppearsAndGoesWithTheApproval(t *testing.T) {
	f := newFix(t)
	id := f.item(TemplateFeature)
	if got := waitingKinds(t, f, nil); len(got) != 0 {
		t.Fatalf("design step waits for nobody: %v", got)
	}
	f.stageAt(id, StepReview)
	if got := waitingKinds(t, f, nil); got[id] != WaitReview {
		t.Fatalf("review step: %v", got)
	}
	// Out of the caller's project scope: not listed.
	if got := waitingKinds(t, f, func(string) bool { return false }); len(got) != 0 {
		t.Fatalf("scoped out: %v", got)
	}
	// The person approves: the flow leaves review, the fact is gone.
	if res, err := f.svc.Approve(id, "human:a"); err != nil || !res.Advanced {
		t.Fatalf("approve = %+v %v", res, err)
	}
	if got := waitingKinds(t, f, nil); len(got) != 0 {
		t.Fatalf("after the approval: %v", got)
	}
	// Sent back to review it waits again; sent back further, it does not.
	if _, err := f.svc.Back(id, "again", "human:a"); err != nil {
		t.Fatal(err)
	}
	if got := waitingKinds(t, f, nil); got[id] != WaitReview {
		t.Fatalf("back to review: %v", got)
	}
	if _, err := f.svc.Back(id, "redo the design", "human:a"); err != nil {
		t.Fatal(err)
	}
	if got := waitingKinds(t, f, nil); len(got) != 0 {
		t.Fatalf("back to design: %v", got)
	}
}

func TestWaitingNeedsHumanGate(t *testing.T) {
	f := newFix(t)
	id := f.item(TemplateSmall)
	f.stageAt(id, StepVerify) // no commit was recorded for doing: only a person can confirm
	facts, err := f.svc.Waiting(nil)
	if err != nil || len(facts) != 1 || facts[0].Kind != WaitNeedsHuman || facts[0].Step != StepVerify || len(facts[0].Missing) == 0 {
		t.Fatalf("facts = %+v %v", facts, err)
	}
	// A blocked flow is the agent's own report: no card.
	if _, err := f.svc.Blocked(id, "waiting", "session:s"); err != nil {
		t.Fatal(err)
	}
	if got := waitingKinds(t, f, nil); len(got) != 0 {
		t.Fatalf("blocked: %v", got)
	}
	// The person confirms: the flow is done and nothing waits.
	if _, err := f.svc.Done(id, "session:s"); err != nil { // unblocks
		t.Fatal(err)
	}
	if res, err := f.svc.Approve(id, "human:a"); err != nil || !res.Advanced {
		t.Fatalf("approve = %+v %v", res, err)
	}
	if got := waitingKinds(t, f, nil); len(got) != 0 {
		t.Fatalf("after the approval: %v", got)
	}
}

func TestWaitingVerifyFailedRun(t *testing.T) {
	w := newVerifyWorld(t)
	// Verify cannot be judged yet only because there is no run: nothing failed.
	if got := waitingKinds(t, w.fix, nil); got[w.id] == WaitVerifyFailed {
		t.Fatalf("no run yet: %v", got)
	}
	// A run before doing finished does not count.
	w.run("wf-early", "verify", jobstore.WorkflowFailed, w.c1)
	w.tick()
	if _, err := w.svc.Back(w.id, "again", "human:a"); err != nil {
		t.Fatal(err)
	}
	w.tick()
	if _, err := w.svc.Done(w.id, "human:a"); err != nil { // re-enter verify, after the run above
		t.Fatal(err)
	}
	if got := waitingKinds(t, w.fix, nil); got[w.id] == WaitVerifyFailed {
		t.Fatalf("a failed run from before doing finished: %v", got)
	}
	w.tick()
	w.run("wf-failed", "verify", jobstore.WorkflowFailed, w.c1)
	facts, err := w.svc.Waiting(nil)
	if err != nil || len(facts) != 1 || facts[0].Kind != WaitVerifyFailed || len(facts[0].Missing) != 1 {
		t.Fatalf("after a failed run: %+v %v", facts, err)
	}
	// A newer passing run clears it (the newest run is what counts).
	w.tick()
	w.run("wf-ok", "verify", jobstore.WorkflowDone, w.c1)
	if got := waitingKinds(t, w.fix, nil); len(got) != 0 {
		t.Fatalf("after a passing run: %v", got)
	}
}
