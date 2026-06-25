package workflow

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// wfStep builds a workflow-type step whose inline sub-workflow runs the given steps.
func wfStep(name string, onFailure string, sub ...StepSpec) StepSpec {
	return StepSpec{
		Name:        name,
		Type:        stepTypeWorkflow,
		OnFailure:   onFailure,
		SubWorkflow: &Spec{Title: name, Steps: sub},
	}
}

// TestWorkflowStepsIncludesSubworkflowStep asserts the step chain surfaces a
// workflow-type step (which runs NO step-job) as its own row carrying type +
// child_workflow_id, so the web/CLI chain shows the sub-workflow and can link into
// it. Regression guard for the P3 UI gap (the whole sub-workflow step was invisible
// in the detail view because WorkflowSteps only returned job-backed steps).
func TestWorkflowStepsIncludesSubworkflowStep(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	wf, err := e.SubmitWorkflow(Spec{Steps: []StepSpec{
		echoStep("gen"),
		wfStep("sub-review", "", echoStep("child-lint")),
	}}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	if final := waitWorkflow(t, e, wf.ID); final.Status != jobstore.WorkflowDone {
		t.Fatalf("workflow status = %s, want done", final.Status)
	}

	steps, err := e.WorkflowSteps(wf.ID)
	if err != nil {
		t.Fatalf("WorkflowSteps: %v", err)
	}
	// step order preserved: job step 1 before workflow step 2.
	if len(steps) < 2 || steps[0].StepIndex != 1 {
		t.Fatalf("chain not step-ordered: %+v", steps)
	}
	var sub *Step
	for i := range steps {
		if steps[i].StepIndex == 2 {
			sub = &steps[i]
		}
	}
	if sub == nil {
		t.Fatalf("step 2 (sub-workflow) missing from chain; got %d rows: %+v", len(steps), steps)
	}
	if sub.Type != stepTypeWorkflow {
		t.Fatalf("step 2 type = %q, want %q", sub.Type, stepTypeWorkflow)
	}
	if want := childWorkflowID(wf.ID, 2, 1); sub.ChildWorkflowID != want {
		t.Fatalf("step 2 child_workflow_id = %q, want %q (chain must link into child)", sub.ChildWorkflowID, want)
	}
	if sub.Status != jobstore.WorkflowDone {
		t.Fatalf("step 2 child status = %q, want done", sub.Status)
	}
}

// waitChildWorkflow polls until the child sub-workflow of (parent, step) exists and is
// terminal, returning it. The child is created by the parent step's startSubWorkflow.
func waitChildWorkflow(t *testing.T, e *Engine, parentID string, step int) jobstore.Workflow {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		child, ok, err := e.meta.FindChildWorkflow(parentID, step)
		if err != nil {
			t.Fatalf("FindChildWorkflow: %v", err)
		}
		if ok && child.Status != jobstore.WorkflowRunning {
			return child
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("child workflow of %s step %d did not reach terminal in time", parentID, step)
	return jobstore.Workflow{}
}

// TestSubWorkflowNestedRunsThroughParent is the P3核心 happy path: a parent workflow
// whose middle step is type=workflow runs the inline sub-workflow to done, and the
// sub-workflow's terminal transition advances the parent to done (父→子→父 done).
func TestSubWorkflowNestedRunsThroughParent(t *testing.T) {
	e := newTestEngine(t, t.TempDir())

	// parent: [job echo, WORKFLOW(2 echo steps), job echo]
	wf, err := e.SubmitWorkflow(Spec{
		Title: "parent",
		Steps: []StepSpec{
			echoStep("p1"),
			wfStep("p2-sub", "", echoStep("c1"), echoStep("c2")),
			echoStep("p3"),
		},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}

	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("parent status = %s (err=%s), want done", final.Status, final.Error)
	}
	// Parent advanced past the last step (TotalSteps+1) like any done workflow.
	if final.CurrentStep != final.TotalSteps+1 {
		t.Fatalf("parent current_step = %d, want %d", final.CurrentStep, final.TotalSteps+1)
	}

	// The step-2 child sub-workflow exists, is bound to (parent, step 2), and is done.
	child, ok, err := e.meta.FindChildWorkflow(wf.ID, 2)
	if err != nil || !ok {
		t.Fatalf("FindChildWorkflow(parent,2): ok=%v err=%v", ok, err)
	}
	if child.Status != jobstore.WorkflowDone {
		t.Fatalf("child status = %s, want done", child.Status)
	}
	if child.ParentWorkflowID != wf.ID || child.ParentStepIndex != 2 {
		t.Fatalf("child parent binding = (%q,%d), want (%q,2)", child.ParentWorkflowID, child.ParentStepIndex, wf.ID)
	}
	if child.CallerID != "alice" {
		t.Fatalf("child caller = %q, want alice (D8 inherit)", child.CallerID)
	}
	// The child id is the deterministic per-attempt id.
	if want := childWorkflowID(wf.ID, 2, 1); child.ID != want {
		t.Fatalf("child id = %q, want %q (deterministic)", child.ID, want)
	}
	// Child ran its 2 inner steps; parent ran step1 (job) + step3 (job), NOT a step-2 job.
	childJobs, _ := e.meta.ListWorkflowJobs(child.ID)
	if len(childJobs) != 2 {
		t.Fatalf("child ran %d step jobs, want 2", len(childJobs))
	}
	parentJobs, _ := e.meta.ListWorkflowJobs(wf.ID)
	for _, j := range parentJobs {
		if j.StepIndex == 2 {
			t.Fatalf("parent started a job at step 2 (workflow-type step should NOT run a job): %s", j.ID)
		}
	}
	if len(parentJobs) != 2 {
		t.Fatalf("parent ran %d step jobs (step1+step3), want 2", len(parentJobs))
	}
}

// TestSubWorkflowFailParentFailFast asserts a failing sub-workflow with on_failure="" /
// fail fails the parent fail-fast: the parent step is failed and the next parent step
// never starts.
func TestSubWorkflowFailParentFailFast(t *testing.T) {
	e := newTestEngine(t, t.TempDir())

	wf, err := e.SubmitWorkflow(Spec{
		Title: "parent-failfast",
		Steps: []StepSpec{
			echoStep("p1"),
			// sub-workflow whose 2nd inner step exits non-zero → child failed.
			wfStep("p2-sub", "", echoStep("c1"), failStep("c2")),
			echoStep("p3-never"),
		},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}

	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowFailed {
		t.Fatalf("parent status = %s, want failed (sub-workflow failed, fail-fast)", final.Status)
	}
	if !strings.Contains(final.Error, "sub-workflow") {
		t.Fatalf("parent error should mention sub-workflow, got: %q", final.Error)
	}

	// The child sub-workflow itself failed.
	child := waitChildWorkflow(t, e, wf.ID, 2)
	if child.Status != jobstore.WorkflowFailed {
		t.Fatalf("child status = %s, want failed", child.Status)
	}
	// Parent step 3 must NOT have started (fail-fast).
	parentJobs, _ := e.meta.ListWorkflowJobs(wf.ID)
	for _, j := range parentJobs {
		if j.StepIndex == 3 {
			t.Fatalf("parent step 3 started despite sub-workflow fail-fast (job %s)", j.ID)
		}
	}
}

// TestSubWorkflowFailContinue asserts a failing sub-workflow with on_failure=continue
// is skipped: the parent advances PAST the failed workflow-type step to the next step
// and completes done.
func TestSubWorkflowFailContinue(t *testing.T) {
	e := newTestEngine(t, t.TempDir())

	wf, err := e.SubmitWorkflow(Spec{
		Title: "parent-continue",
		Steps: []StepSpec{
			echoStep("p1"),
			wfStep("p2-sub", onFailureContinue, echoStep("c1"), failStep("c2")),
			echoStep("p3-runs"),
		},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}

	final := waitWorkflow(t, e, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("parent status = %s (err=%s), want done (failed sub skipped via continue)", final.Status, final.Error)
	}

	// The child failed but the parent skipped it and ran step 3.
	child := waitChildWorkflow(t, e, wf.ID, 2)
	if child.Status != jobstore.WorkflowFailed {
		t.Fatalf("child status = %s, want failed", child.Status)
	}
	parentJobs, _ := e.meta.ListWorkflowJobs(wf.ID)
	if stepJob(parentJobs, 3) == nil {
		t.Fatal("parent step 3 did not run despite on_failure=continue")
	}
}

// TestSubWorkflowSweeperRecoversParentAdvance is the sweeper兜底 proof for nesting: a
// running parent whose workflow-type step's child has ALREADY reached terminal (done)
// but whose parent-advance trigger was lost (crash) must be recovered by the sweeper —
// it re-drives the parent, finds the terminal child via FindChildWorkflow, and advances.
func TestSubWorkflowSweeperRecoversParentAdvance(t *testing.T) {
	e := newTestEngine(t, t.TempDir())

	// Build a parent: [WORKFLOW(1 echo), job echo]. Persist the running parent header
	// pointing at step 1 (the workflow-type step) and a DONE child workflow directly,
	// bypassing the parent-advance trigger — exactly the "trigger lost" crash state.
	sub := Spec{Steps: []StepSpec{echoStep("c1")}}
	parentSpec := Spec{Steps: []StepSpec{
		wfStep("p1-sub", "", echoStep("c1")), // step 1: workflow-type
		echoStep("p2"),                       // step 2: job
	}}
	parentSpec.Steps[0].SubWorkflow = &sub
	parentID := "wf-parent-crash"
	parentJSON := mustMarshalSpec(t, parentSpec)
	if err := e.meta.InsertWorkflow(jobstore.Workflow{
		ID: parentID, Status: jobstore.WorkflowRunning, CurrentStep: 1, StepAttempt: 1, TotalSteps: 2,
		SpecJSON: parentJSON, CallerID: "alice", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("InsertWorkflow parent: %v", err)
	}
	// Persist a DONE child bound to (parent, step 1, attempt 1) with the deterministic id.
	childID := childWorkflowID(parentID, 1, 1)
	if err := e.meta.InsertWorkflow(jobstore.Workflow{
		ID: childID, Status: jobstore.WorkflowDone, CurrentStep: 2, StepAttempt: 1, TotalSteps: 1,
		SpecJSON: mustMarshalSpec(t, sub), CallerID: "alice",
		ParentWorkflowID: parentID, ParentStepIndex: 1, CreatedAt: 2, UpdatedAt: 2,
	}); err != nil {
		t.Fatalf("InsertWorkflow child: %v", err)
	}

	// Before the sweep: parent is still on step 1, no parent step-2 job.
	pj, _ := e.meta.ListWorkflowJobs(parentID)
	if len(pj) != 0 {
		t.Fatalf("pre-sweep parent jobs = %d, want 0", len(pj))
	}

	// Sweep: the sweeper inspects the running parent (and child), re-drives the parent,
	// finds the DONE child, advances to step 2 and runs it → parent done.
	e.AdvanceRunning(context.Background())

	final := waitWorkflow(t, e, parentID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("parent status = %s, want done after sweeper recovery", final.Status)
	}
	pj, _ = e.meta.ListWorkflowJobs(parentID)
	if stepJob(pj, 2) == nil {
		t.Fatal("parent step 2 was not started by the sweeper recovery")
	}
}

// TestSubWorkflowConcurrentParentAdvanceOnce is the 并发硬测: after a child sub-workflow
// reaches terminal, MANY concurrent Advance calls on the PARENT (the lost-and-
// found trigger + the sweeper + duplicates, all racing) must advance the parent step
// EXACTLY ONCE — never start two jobs at the next parent step, never spawn two children.
func TestSubWorkflowConcurrentParentAdvanceOnce(t *testing.T) {
	e := newTestEngine(t, t.TempDir())

	// parent: [WORKFLOW(1 echo), job sleep-marker]. We drive the child to done, freeze
	// the parent on step 1, then hammer parent-advance concurrently and assert step 2
	// (the job step) starts exactly once.
	sub := Spec{Steps: []StepSpec{echoStep("c1")}}
	parentSpec := Spec{Steps: []StepSpec{
		wfStep("p1-sub", "", echoStep("c1")),
		echoStep("p2"),
	}}
	parentSpec.Steps[0].SubWorkflow = &sub
	parentID := "wf-parent-race"
	if err := e.meta.InsertWorkflow(jobstore.Workflow{
		ID: parentID, Status: jobstore.WorkflowRunning, CurrentStep: 1, StepAttempt: 1, TotalSteps: 2,
		SpecJSON: mustMarshalSpec(t, parentSpec), CallerID: "alice", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("InsertWorkflow parent: %v", err)
	}
	childID := childWorkflowID(parentID, 1, 1)
	if err := e.meta.InsertWorkflow(jobstore.Workflow{
		ID: childID, Status: jobstore.WorkflowDone, CurrentStep: 2, StepAttempt: 1, TotalSteps: 1,
		SpecJSON: mustMarshalSpec(t, sub), CallerID: "alice",
		ParentWorkflowID: parentID, ParentStepIndex: 1, CreatedAt: 2, UpdatedAt: 2,
	}); err != nil {
		t.Fatalf("InsertWorkflow child: %v", err)
	}

	// Hammer parent-advance concurrently: real goroutines racing the SAME (1,1)->(2,1)
	// parent transition. The二元组 AdvanceStep抢权 + deterministic request_id keep it once.
	const n = 32
	var started sync.WaitGroup
	started.Add(n)
	begin := make(chan struct{})
	var launched int32
	for i := 0; i < n; i++ {
		go func() {
			atomic.AddInt32(&launched, 1)
			started.Done()
			<-begin
			e.Advance(parentID)
		}()
	}
	started.Wait()
	close(begin)

	final := waitWorkflow(t, e, parentID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("parent status = %s, want done", final.Status)
	}
	if atomic.LoadInt32(&launched) != n {
		t.Fatalf("launched %d advancers, want %d", launched, n)
	}

	// CRUCIAL: parent step 2 started EXACTLY once (no double-start under the race).
	pj, _ := e.meta.ListWorkflowJobs(parentID)
	seen := map[int]int{}
	for _, j := range pj {
		seen[j.StepIndex]++
	}
	if seen[2] != 1 {
		t.Fatalf("parent step 2 started %d times, want exactly 1 (并发幂等 violated)", seen[2])
	}
	// And exactly ONE child workflow exists for (parent, step 1) — no duplicate sub-wf.
	all, _ := e.meta.ListWorkflows("", 0)
	children := 0
	for _, w := range all {
		if w.ParentWorkflowID == parentID && w.ParentStepIndex == 1 {
			children++
		}
	}
	if children != 1 {
		t.Fatalf("found %d children for (parent,step1), want exactly 1 (no duplicate sub-wf)", children)
	}
}

// TestSubWorkflowChildSubmitIdempotent asserts SubmitWorkflowChild is idempotent on the
// deterministic id: two submits for the SAME (parent, step, attempt) create ONE child
// (the second returns the existing child, no error, no duplicate row).
func TestSubWorkflowChildSubmitIdempotent(t *testing.T) {
	e := newTestEngine(t, t.TempDir())

	// A standalone parent header so the child has a real parent_workflow_id.
	if err := e.meta.InsertWorkflow(jobstore.Workflow{
		ID: "wf-idem-parent", Status: jobstore.WorkflowRunning, CurrentStep: 1, StepAttempt: 1,
		TotalSteps: 1, SpecJSON: "{}", CallerID: "alice", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("InsertWorkflow parent: %v", err)
	}

	sub := Spec{Steps: []StepSpec{echoStep("c1")}}
	c1, err := e.SubmitWorkflowChild(sub, "alice", "wf-idem-parent", 1, 1)
	if err != nil {
		t.Fatalf("first SubmitWorkflowChild: %v", err)
	}
	c2, err := e.SubmitWorkflowChild(sub, "alice", "wf-idem-parent", 1, 1)
	if err != nil {
		t.Fatalf("second SubmitWorkflowChild (idempotent): %v", err)
	}
	if c1.ID != c2.ID {
		t.Fatalf("idempotent child id mismatch: %q vs %q", c1.ID, c2.ID)
	}
	if c1.ID != childWorkflowID("wf-idem-parent", 1, 1) {
		t.Fatalf("child id = %q, want deterministic", c1.ID)
	}

	// Exactly one child row exists for (parent, step 1).
	all, _ := e.meta.ListWorkflows("", 0)
	children := 0
	for _, w := range all {
		if w.ParentWorkflowID == "wf-idem-parent" && w.ParentStepIndex == 1 {
			children++
		}
	}
	if children != 1 {
		t.Fatalf("found %d children, want exactly 1 (idempotent submit)", children)
	}

	// Drain the child so its async step job finishes before teardown.
	waitChildWorkflow(t, e, "wf-idem-parent", 1)
}
