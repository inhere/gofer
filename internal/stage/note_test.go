package stage

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// A session on a flow gets its step and position; the object stays within its byte budget
// even for a long blocked reason; and no git is run for it (the fixture's runner fails the
// test if it is called).
func TestBriefForSessionOnAFlowNeverRunsGit(t *testing.T) {
	f := newFix(t)
	f.svc.git = func(string, int, ...string) (string, error) {
		t.Error("the per-turn brief must not run git")
		return "", nil
	}
	if _, err := f.store.UpsertAgentSession(jobstore.AgentSession{SessionID: "s-1", Agent: "claude", ProjectKey: "p", Cwd: f.repo}); err != nil {
		t.Fatal(err)
	}
	v, err := f.svc.Start(StartInput{Issue: "gofer-7", Template: TemplateFeature, ProjectKey: "p", SessionID: "s-1", By: "session:s-1"})
	if err != nil {
		t.Fatal(err)
	}
	b := f.svc.BriefForSession("s-1", "p")
	if b == nil || b.WorkItemID != v.WorkItemID || b.Step != StepDesign || b.StepNo != 1 || b.StepTotal != 6 || b.Status != jobstore.StageActive || b.Enabled {
		t.Fatalf("brief = %+v", b)
	}
	if line := f.svc.TurnNote(i18n.ZhCN, "", "job-x", "s-1"); !strings.Contains(line, "1/6 步「design」") || !strings.HasPrefix(line, "[gofer · 流程]") {
		t.Fatalf("turn note = %q", line)
	}

	if _, err := f.svc.Blocked(v.WorkItemID, strings.Repeat("等评审", 300), "session:s-1"); err != nil {
		t.Fatal(err)
	}
	b = f.svc.BriefForSession("s-1", "p")
	raw, _ := json.Marshal(b)
	if b == nil || b.Status != jobstore.StageBlocked || b.Reason == "" || len(raw) > 300 {
		t.Fatalf("blocked brief = %s", raw)
	}
}

// With no flow, the brief only says the project has the flow on; a project that turned it
// off, a session with no project and a finished flow get nothing.
func TestBriefForSessionWithoutAFlow(t *testing.T) {
	f := newFix(t)
	if b := f.svc.BriefForSession("nobody", "p"); b == nil || !b.Enabled || b.InFlow() {
		t.Fatalf("enabled project, no flow: %+v", b)
	}
	if line := f.svc.TurnNote(i18n.ZhCN, "nobody"); line != "" {
		t.Fatalf("no flow, turn note = %q", line)
	}
	if b := f.svc.BriefForSession("nobody", ""); b != nil {
		t.Fatalf("no project: %+v", b)
	}
	off := false
	f.cfg.Projects["p"] = config.ProjectConfig{HostPath: f.repo, Stage: &config.ProjectStage{Enabled: &off}}
	if b := f.svc.BriefForSession("nobody", "p"); b != nil {
		t.Fatalf("stage.enabled=false: %+v", b)
	}
}

// A flow started before the switch went off still reaches its session.
func TestBriefForSessionKeepsAStartedFlowWhenDisabled(t *testing.T) {
	f := newFix(t)
	if _, err := f.store.UpsertAgentSession(jobstore.AgentSession{SessionID: "s-1", Agent: "claude", ProjectKey: "p", Cwd: f.repo}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Start(StartInput{Issue: "gofer-7", Template: TemplateSmall, ProjectKey: "p", SessionID: "s-1", By: "session:s-1"}); err != nil {
		t.Fatal(err)
	}
	off := false
	f.cfg.Projects["p"] = config.ProjectConfig{HostPath: f.repo, Stage: &config.ProjectStage{Enabled: &off}}
	if b := f.svc.BriefForSession("s-1", "p"); b == nil || !b.InFlow() || b.Step != StepDoing {
		t.Fatalf("brief = %+v", b)
	}
}
