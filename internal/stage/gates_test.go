package stage

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestDesignGate(t *testing.T) {
	f := newFix(t)
	id := f.item(TemplateFeature)

	g := f.gate(id)
	if g.OK || g.Step != StepDesign || !missingHas(g, "design doc") {
		t.Fatalf("no doc linked: %+v", g)
	}

	f.link(id, jobstore.WorkLinkDoc, "docs/design/x.md", jobstore.WorkDocRoleDesign)
	if err := os.WriteFile(filepath.Join(f.repo, "keep"), nil, 0o644); err != nil { // repo needs a commit to have a HEAD
		t.Fatal(err)
	}
	f.commit("README.md", "readme\n")
	// The file exists but is not committed.
	if err := os.MkdirAll(filepath.Join(f.repo, "docs/design"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, "docs/design/x.md"), []byte("# x\ngofer-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g = f.gate(id)
	if g.OK || g.NeedsHuman || !missingHas(g, "docs/design/x.md", "not committed") {
		t.Fatalf("uncommitted doc: %+v", g)
	}

	// Committed, but the issue id is only past line 20.
	f.commit("docs/design/x.md", lines(25, "gofer-1\n"))
	g = f.gate(id)
	if g.OK || !missingHas(g, "must name the issue", "gofer-1") {
		t.Fatalf("issue id past line 20: %+v", g)
	}

	// Committed with the id in the first lines: the gate passes.
	f.commit("docs/design/x.md", "# design\n> issue: gofer-1\n"+lines(30, ""))
	g = f.gate(id)
	if !g.OK || g.NeedsHuman || len(g.Missing) != 0 {
		t.Fatalf("committed with issue id: %+v", g)
	}

	// A path that escapes the repository is refused with a clear message.
	f.link(id, jobstore.WorkLinkDoc, "../outside.md", jobstore.WorkDocRoleDesign)
	g = f.gate(id)
	if g.OK || !missingHas(g, "repository-relative") {
		t.Fatalf("escaping path: %+v", g)
	}
}

func TestDesignGateNeedsHumanWhenGitCannotBeRead(t *testing.T) {
	f := newFix(t)
	id := f.item(TemplateFeature)
	f.link(id, jobstore.WorkLinkDoc, "docs/design/x.md", jobstore.WorkDocRoleDesign)

	// The project root is not a git work tree on this machine.
	f.cfg.Projects["p"] = config.ProjectConfig{HostPath: filepath.Join(t.TempDir(), "no-such-dir")}
	g := f.gate(id)
	if g.OK || !g.NeedsHuman || !missingHas(g, "git is not readable") {
		t.Fatalf("unreadable project: %+v", g)
	}

	// An item without a project cannot be judged either.
	f.cfg.Projects["p"] = config.ProjectConfig{HostPath: f.repo}
	f.cfg.Projects = map[string]config.ProjectConfig{}
	g = f.gate(id)
	if g.OK || !g.NeedsHuman {
		t.Fatalf("unregistered project: %+v", g)
	}

	// A person's approval waives a gate that cannot be judged: the flow moves on.
	res, err := f.svc.Approve(id, "human:a")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Advanced || res.Stage.Step != StepReview {
		t.Fatalf("approve of an unjudgeable gate: %+v", res)
	}
}

func TestReviewGate(t *testing.T) {
	f := newFix(t)
	id := f.item(TemplateFeature)
	f.stageAt(id, StepReview)

	g := f.gate(id)
	if g.OK || g.NeedsHuman || !missingHas(g, "review job", "approve") {
		t.Fatalf("no review job: %+v", g)
	}

	// A job linked with role review that has not reported is not a pass.
	f.reviewJob("r1", "")
	f.link(id, jobstore.WorkLinkJob, "r1", jobstore.WorkJobRoleReview)
	g = f.gate(id)
	if g.OK || !missingHas(g, "r1", "not reported") {
		t.Fatalf("unfinished review job: %+v", g)
	}

	// A review that asked for rework is round 1 of 2: still not through, a person is not needed yet.
	f.tick()
	f.reviewJob("r2", "rework")
	f.link(id, jobstore.WorkLinkJob, "r2", jobstore.WorkJobRoleReview)
	g = f.gate(id)
	if g.OK {
		t.Fatalf("rework review: %+v", g)
	}
}

func TestReviewGatePassAndRoundLimit(t *testing.T) {
	f := newFix(t)
	id := f.item(TemplateFeature)
	f.stageAt(id, StepReview)

	// A pass reported by a review job linked after entering the step passes the gate.
	f.reviewJob("p1", "pass")
	f.link(id, jobstore.WorkLinkJob, "p1", jobstore.WorkJobRoleReview)
	if g := f.gate(id); !g.OK {
		t.Fatalf("pass review: %+v", g)
	}

	// Two failed rounds use the limit up: the third job's pass no longer counts, only a person can.
	f2 := newFix(t)
	id2 := f2.item(TemplateFeature)
	f2.stageAt(id2, StepReview)
	for i, d := range []string{"rework", "fail", "pass"} {
		f2.tick()
		jid := string(rune('a'+i)) + "-job"
		f2.reviewJob(jid, d)
		f2.link(id2, jobstore.WorkLinkJob, jid, jobstore.WorkJobRoleReview)
	}
	g := f2.gate(id2)
	if g.OK || !g.NeedsHuman || !missingHas(g, "used up", "max 2") {
		t.Fatalf("rounds used up: %+v", g)
	}
	res, err := f2.svc.Approve(id2, "human:a")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Advanced || res.Stage.Step != StepPlan {
		t.Fatalf("approve after the limit: %+v", res)
	}
}

func TestReviewGateIgnoresJobsLinkedBeforeTheStep(t *testing.T) {
	f := newFix(t)
	id := f.item(TemplateFeature)
	f.reviewJob("old", "pass")
	f.link(id, jobstore.WorkLinkJob, "old", jobstore.WorkJobRoleReview)
	f.tick()
	f.stageAt(id, StepReview) // entered after the link
	if g := f.gate(id); g.OK {
		t.Fatalf("a pass from before the review step must not count: %+v", g)
	}
}

func TestPlanGate(t *testing.T) {
	f := newFix(t)
	id := f.item(TemplateFeature)
	f.stageAt(id, StepPlan)

	if g := f.gate(id); g.OK || !missingHas(g, "link a gofer plan") {
		t.Fatalf("no plan: %+v", g)
	}

	if err := f.store.InsertPlan(jobstore.Plan{PlanID: "plan-00001", Title: "t", ProjectKey: "p"}); err != nil {
		t.Fatal(err)
	}
	f.link(id, jobstore.WorkLinkPlan, "plan-00001", "")
	if g := f.gate(id); g.OK || !missingHas(g, "plan-00001", "no todo") {
		t.Fatalf("plan without todos: %+v", g)
	}

	if err := f.store.InsertTodo(jobstore.PlanTodo{TodoID: "todo-0001", PlanID: "plan-00001", Title: "a", Status: jobstore.TodoPending}); err != nil {
		t.Fatal(err)
	}
	g := f.gate(id)
	if g.OK || !missingHas(g, "plan doc") {
		t.Fatalf("feature needs a plan doc: %+v", g)
	}

	f.link(id, jobstore.WorkLinkDoc, "docs/plans/p.md", jobstore.WorkDocRolePlan)
	f.commit("README.md", "x\n")
	if g := f.gate(id); g.OK || !missingHas(g, "docs/plans/p.md", "not committed") {
		t.Fatalf("uncommitted plan doc: %+v", g)
	}
	f.commit("docs/plans/p.md", "plan\n")
	if g := f.gate(id); !g.OK {
		t.Fatalf("plan with todo and committed doc: %+v", g)
	}
}

func TestDoingGate(t *testing.T) {
	f := newFix(t)
	id := f.item(TemplateFeature)
	f.stageAt(id, StepDoing)

	if g := f.gate(id); g.OK || !missingHas(g, "link a gofer plan") {
		t.Fatalf("feature doing without a plan: %+v", g)
	}

	if err := f.store.InsertPlan(jobstore.Plan{PlanID: "plan-00001", Title: "t", ProjectKey: "p"}); err != nil {
		t.Fatal(err)
	}
	f.link(id, jobstore.WorkLinkPlan, "plan-00001", "")
	for _, td := range []jobstore.PlanTodo{
		{TodoID: "todo-000a", PlanID: "plan-00001", Title: "a", Status: jobstore.TodoDone, Done: true},
		{TodoID: "todo-000b", PlanID: "plan-00001", Title: "b", Status: jobstore.TodoSkipped},
		{TodoID: "todo-000c", PlanID: "plan-00001", Title: "c", Status: jobstore.TodoDoing},
	} {
		if err := f.store.InsertTodo(td); err != nil {
			t.Fatal(err)
		}
	}
	g := f.gate(id)
	if g.OK || !missingHas(g, "todo-000c", "doing") || missingHas(g, "todo-000a") || missingHas(g, "todo-000b") {
		t.Fatalf("unfinished todo must be named, finished and skipped ones not: %+v", g)
	}

	if _, err := f.store.UpdateTodoStatus("todo-000c", jobstore.TodoDone, nil); err != nil {
		t.Fatal(err)
	}
	if g := f.gate(id); !g.OK {
		t.Fatalf("done and skipped todos: %+v", g)
	}

	// A small flow with no plan has nothing to wait for.
	f2 := newFix(t)
	id2 := f2.item(TemplateSmall)
	if g := f2.gate(id2); !g.OK || g.Step != StepDoing {
		t.Fatalf("small doing without a plan: %+v", g)
	}
}

// verifyWorld is a repository with commits c0 < c1 (c1 is where doing finished) < c2, and a
// flow of the small template standing on verify with doing_done_sha = c1.
type verifyWorld struct {
	*fix
	id         string
	c0, c1, c2 string
}

func newVerifyWorld(t *testing.T) *verifyWorld {
	f := newFix(t)
	w := &verifyWorld{fix: f}
	w.c0 = f.commit("a.txt", "0\n")
	w.id = f.item(TemplateSmall)
	w.c1 = f.commit("a.txt", "1\n")
	f.tick()
	if _, err := f.svc.Done(w.id, "human:a"); err != nil { // doing -> verify, records HEAD = c1
		t.Fatal(err)
	}
	f.tick()
	w.c2 = f.commit("a.txt", "2\n")
	ws, _, _ := f.store.GetWorkStage(w.id)
	if ws.Step != 1 || ws.DoingDoneSHA != w.c1 {
		t.Fatalf("setup: step %d sha %q want %q", ws.Step, ws.DoingDoneSHA, w.c1)
	}
	return w
}

// run records a workflow run of template tpl with status, created at the current clock, whose
// first step job started on base.
func (w *verifyWorld) run(wfID, tpl, status, base string) {
	w.t.Helper()
	if err := w.store.InsertWorkflow(jobstore.Workflow{
		ID: wfID, Title: wfID, Status: status, TotalSteps: 1, SpecJSON: `{"project":"p","steps":[{"project_key":"p"}]}`,
		CreatedAt: w.clock.Load(), UpdatedAt: w.clock.Load(), Template: tpl,
	}); err != nil {
		w.t.Fatal(err)
	}
	if err := w.store.UpsertJob(jobstore.JobRecord{ID: wfID + "-j1", ProjectKey: "p", Agent: "exec", Runner: "local", Status: "done", WorkflowID: wfID, StepIndex: 1, BaseSHA: base}); err != nil {
		w.t.Fatal(err)
	}
}

func TestVerifyGateAncestor(t *testing.T) {
	w := newVerifyWorld(t)

	if g := w.gate(w.id); g.OK || g.NeedsHuman || !missingHas(g, "no finished run", "verify") {
		t.Fatalf("no run at all: %+v", g)
	}

	// A finished run on the commit doing finished at passes.
	w.tick()
	w.run("wf-same", "verify", jobstore.WorkflowDone, w.c1)
	if g := w.gate(w.id); !g.OK {
		t.Fatalf("run on the same commit: %+v", g)
	}
}

func TestVerifyGateAcceptsDescendantAndRejectsAncestor(t *testing.T) {
	w := newVerifyWorld(t)
	w.tick()
	// Reverse case: a run on c0, which is BEFORE the commit doing finished at, does not count.
	w.run("wf-old", "verify", jobstore.WorkflowDone, w.c0)
	g := w.gate(w.id)
	if g.OK || g.NeedsHuman || !missingHas(g, "do not contain") {
		t.Fatalf("run on an older commit must not pass: %+v", g)
	}
	// A run on a descendant of it does.
	w.tick()
	w.run("wf-new", "verify", jobstore.WorkflowDone, w.c2)
	if g := w.gate(w.id); !g.OK {
		t.Fatalf("run on a descendant: %+v", g)
	}
}

func TestVerifyGateFiltersRuns(t *testing.T) {
	w := newVerifyWorld(t)
	w.tick()
	w.run("wf-other-template", "lint", jobstore.WorkflowDone, w.c1)
	w.run("wf-running", "verify", jobstore.WorkflowRunning, w.c1)
	w.run("wf-failed", "verify", "failed", w.c1)
	if g := w.gate(w.id); g.OK {
		t.Fatalf("other template / unfinished / failed runs must not pass: %+v", g)
	}

	// verify_templates picks the template: with lint listed, the lint run counts.
	w.cfg.Projects["p"] = config.ProjectConfig{HostPath: w.repo, Stage: &config.ProjectStage{VerifyTemplates: []string{"lint"}}}
	if g := w.gate(w.id); !g.OK {
		t.Fatalf("configured verify template: %+v", g)
	}
}

func TestVerifyGateRunStartedBeforeDoingFinishedDoesNotCount(t *testing.T) {
	f := newFix(t)
	f.commit("a.txt", "0\n")
	id := f.item(TemplateSmall)
	c1 := f.commit("a.txt", "1\n")
	w := &verifyWorld{fix: f, id: id, c1: c1}
	w.run("wf-early", "verify", jobstore.WorkflowDone, c1) // created at the current clock, before doing is done
	f.tick()
	if _, err := f.svc.Done(id, "human:a"); err != nil {
		t.Fatal(err)
	}
	if g := f.gate(id); g.OK {
		t.Fatalf("a run that started before doing finished must not count: %+v", g)
	}
}

func TestVerifyGateNeedsHuman(t *testing.T) {
	w := newVerifyWorld(t)
	w.tick()

	// A run whose base commit is unknown to this repository cannot be compared.
	w.run("wf-foreign", "verify", jobstore.WorkflowDone, "0123456789abcdef0123456789abcdef01234567")
	g := w.gate(w.id)
	if g.OK || !g.NeedsHuman || !missingHas(g, "could not be matched") {
		t.Fatalf("unknown base commit: %+v", g)
	}

	// A flow whose doing commit was never recorded cannot be judged.
	f := newFix(t)
	id := f.item(TemplateSmall) // no commit in the repo: HEAD cannot be read, so no sha is recorded
	if _, err := f.svc.Done(id, "human:a"); err != nil {
		t.Fatal(err)
	}
	g = f.gate(id)
	if g.OK || !g.NeedsHuman || !missingHas(g, "not recorded") {
		t.Fatalf("no recorded doing commit: %+v", g)
	}

	// git missing from the path: still no error, only needs_human.
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty"))
	if _, err := exec.LookPath("git"); err == nil {
		t.Skip("git still resolvable")
	}
	g = w.gate(w.id)
	if g.OK || !g.NeedsHuman {
		t.Fatalf("git unavailable: %+v", g)
	}
}

func TestDoneStepHasNoGate(t *testing.T) {
	f := newFix(t)
	id := f.item(TemplateSmall)
	f.stageAt(id, StepDone)
	v, err := f.svc.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if v.Gate == nil || !v.Gate.OK || len(v.Gate.Missing) != 0 {
		t.Fatalf("done step gate: %+v", v.Gate)
	}
}
