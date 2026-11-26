package stage

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestStartFindsOrCreatesTheItemOfAnIssue(t *testing.T) {
	f := newFix(t)
	if _, err := f.store.UpsertAgentSession(jobstore.AgentSession{SessionID: "s-1", Agent: "claude", ProjectKey: "p", Cwd: f.repo}); err != nil {
		t.Fatal(err)
	}

	v, err := f.svc.Start(StartInput{Issue: "gofer-7", Template: TemplateSmall, ProjectKey: "p", Title: "fix it", SessionID: "s-1", By: "session:s-1"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Template != TemplateSmall || v.Step != StepDoing || v.Status != jobstore.StageActive || v.ProjectKey != "p" {
		t.Fatalf("view = %+v", v)
	}
	// A new item was created, linked to the issue, with the caller's session as current.
	links, _ := f.store.ListWorkLinksRole(v.WorkItemID, jobstore.WorkLinkIssue)
	if len(links) != 1 || links[0].Ref != "gofer-7" {
		t.Fatalf("issue link = %+v", links)
	}
	cur, _ := f.store.ListWorkItemSessions(v.WorkItemID)
	if len(cur) != 1 || cur[0].SessionID != "s-1" || cur[0].Role != jobstore.WorkSessionCurrent {
		t.Fatalf("sessions = %+v", cur)
	}
	// ForSession finds it, without and with the gate.
	got, ok, err := f.svc.ForSession("s-1", false)
	if err != nil || !ok || got.WorkItemID != v.WorkItemID || got.Gate != nil {
		t.Fatalf("ForSession(false) = %+v %v %v", got, ok, err)
	}
	got, ok, err = f.svc.ForSession("s-1", true)
	if err != nil || !ok || got.Gate == nil {
		t.Fatalf("ForSession(true) = %+v %v %v", got, ok, err)
	}
	if _, ok, _ := f.svc.ForSession("nobody", false); ok {
		t.Fatal("a session that is on no flow must get none")
	}

	// A second start on the same item is refused.
	if _, err := f.svc.Start(StartInput{Issue: "gofer-7", By: "human:a"}); !errors.Is(err, jobstore.ErrStageActive) {
		t.Fatalf("second start err = %v, want ErrStageActive", err)
	}

	// Another issue's start reuses an existing open item that is linked to it.
	it, err := f.store.CreateWorkItem(jobstore.WorkItemInput{Title: "pre-existing", ProjectKey: "p", By: "human:a"})
	if err != nil {
		t.Fatal(err)
	}
	f.link(it.ID, jobstore.WorkLinkIssue, "gofer-8", "")
	v2, err := f.svc.Start(StartInput{Issue: "gofer-8", Template: TemplateFeature, By: "human:a"})
	if err != nil {
		t.Fatal(err)
	}
	if v2.WorkItemID != it.ID || v2.Step != StepDesign {
		t.Fatalf("start on a linked issue = %+v, want item %s", v2, it.ID)
	}

	// Nothing to start on, and unknown templates, are 400-class errors.
	if _, err := f.svc.Start(StartInput{}); !errors.Is(err, jobstore.ErrWorkInvalid) {
		t.Fatalf("empty start err = %v", err)
	}
	if _, err := f.svc.Start(StartInput{Issue: "gofer-9", Template: "huge"}); !errors.Is(err, jobstore.ErrWorkInvalid) {
		t.Fatalf("unknown template err = %v", err)
	}
	if _, err := f.svc.Start(StartInput{WorkItemID: "w-nope"}); !errors.Is(err, jobstore.ErrWorkItemNotFound) {
		t.Fatalf("unknown work item err = %v", err)
	}
}

func TestTemplateStepsAreSnapshotted(t *testing.T) {
	f := newFix(t)
	id := f.item(TemplateFeature)

	saved := builtinTemplates[0].Steps
	builtinTemplates[0].Steps = []string{StepDoing, StepDone}
	t.Cleanup(func() { builtinTemplates[0].Steps = saved })

	v, err := f.svc.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(v.Steps, []string{StepDesign, StepReview, StepPlan, StepDoing, StepVerify, StepDone}) {
		t.Fatalf("a flow in progress must keep the steps it started with: %v", v.Steps)
	}
	if v.ReviewMax != DefaultReviewMax {
		t.Fatalf("review max = %d", v.ReviewMax)
	}
}

func TestSmallFlowRunsToTheEnd(t *testing.T) {
	f := newFix(t)
	f.commit("a.txt", "0\n")
	id := f.item(TemplateSmall)
	head := f.commit("a.txt", "1\n")
	f.tick()

	res, err := f.svc.Done(id, "session:s")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Advanced || res.From != StepDoing || res.Stage.Step != StepVerify || res.Stage.DoingDoneSHA != head {
		t.Fatalf("doing done = %+v", res)
	}

	// The verify gate is unmet: the flow stays on verify and says what is missing.
	f.tick()
	res, err = f.svc.Done(id, "session:s")
	if err != nil {
		t.Fatal(err)
	}
	if res.Advanced || res.Stage.Step != StepVerify || len(res.Gate.Missing) == 0 {
		t.Fatalf("verify done without a run = %+v", res)
	}

	w := &verifyWorld{fix: f, id: id, c1: head}
	f.tick()
	w.run("wf-1", "verify", jobstore.WorkflowDone, head)
	res, err = f.svc.Done(id, "session:s")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Advanced || res.Stage.Status != jobstore.StageDone || res.Stage.Step != StepDone {
		t.Fatalf("verify done = %+v", res)
	}
	if _, err := f.svc.Done(id, "session:s"); !errors.Is(err, jobstore.ErrStageClosed) {
		t.Fatalf("done on a finished flow err = %v", err)
	}

	// Every step change and the failed gate are in the journal, kind stage.
	j, _ := f.store.ListWorkJournal(id, 0, 0)
	var texts []string
	for _, e := range j {
		if e.Kind == jobstore.WorkJournalStage {
			texts = append(texts, e.Text)
		}
	}
	all := strings.Join(texts, "\n")
	for _, want := range []string{"stage start: small", "stage step: doing > verify", "stage gate not passed at verify", "stage step: verify > done"} {
		if !strings.Contains(all, want) {
			t.Errorf("journal lacks %q:\n%s", want, all)
		}
	}
}

func TestConcurrentDoneAdvancesOnce(t *testing.T) {
	f := newFix(t)
	id := f.item(TemplateSmall) // doing has no plan: its gate passes

	const racers = 8
	var advanced, moved atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := f.svc.Done(id, "session:s")
			if err != nil {
				t.Errorf("done: %v", err)
				return
			}
			if res.Advanced {
				advanced.Add(1)
			}
			if res.Moved {
				moved.Add(1)
			}
		}()
	}
	wg.Wait()
	// Late callers that read the flow after the winner see verify's (unmet) gate instead of
	// "moved"; what matters is that doing was passed exactly once and verify was not skipped.
	if advanced.Load() != 1 {
		t.Fatalf("advanced = %d, want exactly 1", advanced.Load())
	}
	ws, _, _ := f.store.GetWorkStage(id)
	if ws.Step != 1 || ws.Status != jobstore.StageActive {
		t.Fatalf("flow after the race: step %d status %s, want verify/active", ws.Step, ws.Status)
	}
	j, _ := f.store.ListWorkJournal(id, 0, 0)
	steps := 0
	for _, e := range j {
		if e.Kind == jobstore.WorkJournalStage && strings.Contains(e.Text, "stage step: doing > verify") {
			steps++
		}
	}
	if steps != 1 {
		t.Fatalf("step journal lines = %d, want 1", steps)
	}
}

func TestBackAndBlockedRoundTrip(t *testing.T) {
	f := newFix(t)
	id := f.item(TemplateFeature)

	if _, err := f.svc.Back(id, "x", "human:a"); !errors.Is(err, jobstore.ErrWorkInvalid) {
		t.Fatalf("back at the first step err = %v", err)
	}
	f.stageAt(id, StepPlan)
	if _, err := f.svc.Back(id, "  ", "human:a"); !errors.Is(err, jobstore.ErrWorkInvalid) {
		t.Fatalf("back without a reason err = %v", err)
	}
	v, err := f.svc.Back(id, "the design changed", "session:s")
	if err != nil {
		t.Fatal(err)
	}
	if v.Step != StepReview || v.Status != jobstore.StageActive {
		t.Fatalf("after back = %+v", v)
	}
	j, _ := f.store.ListWorkJournal(id, 0, 0)
	// The back line is followed by the would-do line of the step it entered.
	if back := j[len(j)-2]; !strings.Contains(back.Text, "stage back: plan > review: the design changed") {
		t.Fatalf("back journal = %q", back.Text)
	}

	// Blocked needs a reason, shows in the view, and a later Done lifts it (the gate is
	// still evaluated: the flow stays on the step when it is unmet).
	if _, err := f.svc.Blocked(id, "", "session:s"); !errors.Is(err, jobstore.ErrWorkInvalid) {
		t.Fatalf("blocked without a reason err = %v", err)
	}
	v, err = f.svc.Blocked(id, "waiting for the reviewer", "session:s")
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != jobstore.StageBlocked || v.BlockedReason != "waiting for the reviewer" || v.Step != StepReview {
		t.Fatalf("after blocked = %+v", v)
	}
	res, err := f.svc.Done(id, "session:s")
	if err != nil {
		t.Fatal(err)
	}
	if res.Advanced || res.Stage.Status != jobstore.StageActive || res.Stage.BlockedReason != "" || res.Stage.Step != StepReview {
		t.Fatalf("done on a blocked flow with an unmet gate = %+v", res)
	}

	// Blocked, then a passing gate: Done unblocks and advances in one call.
	f.reviewJob("ok-1", "pass")
	f.link(id, jobstore.WorkLinkJob, "ok-1", jobstore.WorkJobRoleReview)
	if _, err := f.svc.Blocked(id, "hold on", "session:s"); err != nil {
		t.Fatal(err)
	}
	res, err = f.svc.Done(id, "session:s")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Advanced || res.Stage.Step != StepPlan || res.Stage.Status != jobstore.StageActive {
		t.Fatalf("done on a blocked flow with a passing gate = %+v", res)
	}
}

func TestApproveIsRecordedButDoesNotWaiveAnUnmetGate(t *testing.T) {
	f := newFix(t)
	id := f.item(TemplateFeature) // design: no doc, no issue-named doc
	res, err := f.svc.Approve(id, "human:a")
	if err != nil {
		t.Fatal(err)
	}
	if res.Advanced || res.Stage.Step != StepDesign || !missingHas(res.Gate, "design doc") {
		t.Fatalf("approve of a plainly unmet gate = %+v", res)
	}

	// The review gate is the approval itself.
	f.stageAt(id, StepReview)
	res, err = f.svc.Approve(id, "human:a")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Advanced || res.Stage.Step != StepPlan {
		t.Fatalf("approve at review = %+v", res)
	}
	// The approval does not carry over to the next step.
	if res.Stage.Approved {
		t.Fatal("an approval must not carry into the next step")
	}
}

func TestDropAndClosedFlows(t *testing.T) {
	f := newFix(t)
	id := f.item(TemplateSmall)
	v, err := f.svc.Drop(id, "not needed", "human:a")
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != jobstore.StageDropped {
		t.Fatalf("drop = %+v", v)
	}
	for name, call := range map[string]func() error{
		"done":    func() error { _, err := f.svc.Done(id, "x"); return err },
		"back":    func() error { _, err := f.svc.Back(id, "r", "x"); return err },
		"blocked": func() error { _, err := f.svc.Blocked(id, "r", "x"); return err },
		"approve": func() error { _, err := f.svc.Approve(id, "x"); return err },
		"drop":    func() error { _, err := f.svc.Drop(id, "r", "x"); return err },
	} {
		if err := call(); !errors.Is(err, jobstore.ErrStageClosed) {
			t.Errorf("%s on a dropped flow err = %v, want ErrStageClosed", name, err)
		}
	}
	if _, err := f.svc.Status("w-none"); !errors.Is(err, jobstore.ErrWorkItemNotFound) {
		t.Errorf("status of an unknown item err = %v", err)
	}
	it, _ := f.store.CreateWorkItem(jobstore.WorkItemInput{Title: "no flow", By: "human:a"})
	if _, err := f.svc.Status(it.ID); !errors.Is(err, jobstore.ErrStageNotFound) {
		t.Errorf("status of an item without a flow err = %v", err)
	}
	// A dropped flow can be started again.
	if _, err := f.svc.Start(StartInput{WorkItemID: id, Template: TemplateFeature, By: "human:a"}); err != nil {
		t.Fatalf("restart after drop: %v", err)
	}
}

func TestEnabledFollowsTheProjectConfig(t *testing.T) {
	f := newFix(t)
	if !f.svc.Enabled("p") || !f.svc.Enabled("unknown") {
		t.Fatal("stage must default to enabled")
	}
	off := false
	f.cfg.Projects["p"] = config.ProjectConfig{HostPath: f.repo, Stage: &config.ProjectStage{Enabled: &off}}
	if f.svc.Enabled("p") {
		t.Fatal("stage.enabled=false must read as disabled")
	}
	// A flow started before the switch keeps working.
	f.cfg.Projects["p"] = config.ProjectConfig{HostPath: f.repo}
	id := f.item(TemplateSmall)
	f.cfg.Projects["p"] = config.ProjectConfig{HostPath: f.repo, Stage: &config.ProjectStage{Enabled: &off}}
	if _, err := f.svc.Done(id, "x"); err != nil {
		t.Fatalf("a started flow must not be affected by enabled=false: %v", err)
	}
}

func TestListOpen(t *testing.T) {
	f := newFix(t)
	a := f.item(TemplateSmall)
	vs, err := f.svc.ListOpen("p")
	if err != nil || len(vs) != 1 || vs[0].WorkItemID != a {
		t.Fatalf("ListOpen = %+v %v", vs, err)
	}
	if vs, _ := f.svc.ListOpen("other"); len(vs) != 0 {
		t.Fatalf("ListOpen(other) = %+v", vs)
	}
}
