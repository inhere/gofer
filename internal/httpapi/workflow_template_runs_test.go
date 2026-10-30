package httpapi

import (
	"fmt"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"io"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job/workflow"
)

const runsTemplateYAML = `title: "runs check"
steps:
  - name: check
    agent: exec
    cmd: ["echo", "ok"]
    cwd: .
    timeout_sec: 60
`

type runsWorkflowSummary struct {
	ID             string `json:"id"`
	Template       string `json:"template"`
	TemplateSource string `json:"template_source"`
}

// submitRunsWorkflow posts body to /v1/workflows with token and returns the created header.
func submitRunsWorkflow(t *testing.T, s *Server, token string, body any) runsWorkflowSummary {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/workflows", token, body)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("submit status = %d (%s), want 200", resp.StatusCode, b)
	}
	var wf runsWorkflowSummary
	decode(t, resp, &wf)
	return wf
}

func listRunsWorkflows(t *testing.T, s *Server, token, query string) []runsWorkflowSummary {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/workflows"+query, token, nil)
	var out struct {
		Workflows []runsWorkflowSummary `json:"workflows"`
		Total     int                   `json:"total"`
	}
	decode(t, resp, &out)
	if out.Total != len(out.Workflows) {
		t.Fatalf("total = %d with %d rows, want them equal", out.Total, len(out.Workflows))
	}
	return out.Workflows
}

func listRunsTemplates(t *testing.T, s *Server, token string) map[string]workflow.WorkflowTemplate {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/workflow-templates?project=seed", token, nil)
	var out struct {
		Templates []workflow.WorkflowTemplate `json:"templates"`
	}
	decode(t, resp, &out)
	byName := map[string]workflow.WorkflowTemplate{}
	for _, tpl := range out.Templates {
		byName[tpl.Name] = tpl
	}
	return byName
}

// TestWorkflowTemplateAttribution pins that a template submit records the template name and
// its origin on the run, a direct spec submit leaves both empty, and GET /v1/workflows
// ?template= keeps only the runs of that template.
func TestWorkflowTemplateAttribution(t *testing.T) {
	s, _, _ := newTemplateTestServer(t)
	resp := do(t, s, http.MethodPut, "/v1/workflow-templates/op-runs?scope=project&project=seed", adminToken,
		map[string]string{"content": runsTemplateYAML})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save template status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	proj := submitRunsWorkflow(t, s, adminToken, map[string]any{"template": "op-runs", "project": "seed"})
	if proj.Template != "op-runs" || proj.TemplateSource != "project" {
		t.Fatalf("project template run = %+v, want op-runs / project", proj)
	}
	resp = do(t, s, http.MethodPut, "/v1/workflow-templates/op-global?scope=global", adminToken,
		map[string]string{"content": runsTemplateYAML})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save global template status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	global := submitRunsWorkflow(t, s, adminToken, map[string]any{"template": "op-global", "project": "seed"})
	if global.Template != "op-global" || global.TemplateSource != "global" {
		t.Fatalf("global template run = %+v, want op-global / global", global)
	}
	if got := templateSourceOf("verify", "", ""); got != "builtin" {
		t.Fatalf("templateSourceOf(verify) = %q, want builtin", got)
	}
	direct := submitRunsWorkflow(t, s, adminToken, map[string]any{
		"project": "seed",
		"steps":   []map[string]any{{"name": "check", "agent": "exec", "cmd": []string{"echo", "ok"}, "cwd": ".", "timeout_sec": 60}},
	})
	if direct.Template != "" || direct.TemplateSource != "" {
		t.Fatalf("direct spec run = %+v, want empty template", direct)
	}

	got := listRunsWorkflows(t, s, adminToken, "?template=op-runs")
	if len(got) != 1 || got[0].ID != proj.ID || got[0].Template != "op-runs" {
		t.Fatalf("?template=op-runs = %+v, want only %s", got, proj.ID)
	}
	if got := listRunsWorkflows(t, s, adminToken, "?template=nope"); len(got) != 0 {
		t.Fatalf("?template=nope = %+v, want none", got)
	}
	if got := listRunsWorkflows(t, s, adminToken, ""); len(got) != 3 {
		t.Fatalf("unfiltered list = %d rows, want 3", len(got))
	}

	// The detail endpoint carries the attribution too.
	resp = do(t, s, http.MethodGet, "/v1/workflows/"+proj.ID, adminToken, nil)
	var detail runsWorkflowSummary
	decode(t, resp, &detail)
	if detail.Template != "op-runs" || detail.TemplateSource != "project" {
		t.Fatalf("detail = %+v, want op-runs / project", detail)
	}
}

// TestWorkflowTemplateListRuns pins the runs / last_run summary on the template list: zero
// runs without last_run, then counts that only include the runs the caller may see.
func TestWorkflowTemplateListRuns(t *testing.T) {
	s, _, _ := newTemplateTestServer(t)
	resp := do(t, s, http.MethodPut, "/v1/workflow-templates/op-runs?scope=project&project=seed", adminToken,
		map[string]string{"content": runsTemplateYAML})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save template status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	tpls := listRunsTemplates(t, s, adminToken)
	if tpl, ok := tpls["op-runs"]; !ok || tpl.Runs != 0 || tpl.LastRun != nil {
		t.Fatalf("before any run: %+v (present %v), want runs 0 and no last_run", tpl, ok)
	}

	adminRun := submitRunsWorkflow(t, s, adminToken, map[string]any{"template": "op-runs", "project": "seed"})
	admin := listRunsTemplates(t, s, adminToken)["op-runs"]
	if admin.Runs != 1 || admin.LastRun == nil || admin.LastRun.ID != adminRun.ID || admin.LastRun.StartedAt == 0 || admin.LastRun.Status == "" {
		t.Fatalf("admin view = %+v last %+v, want 1 run and its last_run", admin, admin.LastRun)
	}
	// An untouched template keeps runs 0.
	if v := listRunsTemplates(t, s, adminToken)["verify"]; v.Runs != 0 || v.LastRun != nil {
		t.Fatalf("verify = %+v, want no runs", v)
	}
}

// TestWorkflowTemplateRunsVisibility pins that runs the caller cannot list are not counted:
// a user without job.read.others counts only its own runs, a project ceiling only its
// projects', and the newest visible run is the last_run.
func TestWorkflowTemplateRunsVisibility(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
		},
	})
	s.setDBGrants(grantsFor(nil))
	meta := s.jobs.Meta()
	now := time.Now().Unix()
	seed := func(id, caller, tpl, status string, at int64) {
		if err := meta.InsertWorkflow(jobstore.Workflow{
			ID: id, Status: status, CallerID: caller, CurrentStep: 1, TotalSteps: 1, SpecJSON: "{}",
			Template: tpl, TemplateSource: "builtin", CreatedAt: at, UpdatedAt: at + 5,
		}); err != nil {
			t.Fatalf("seed workflow %s: %v", id, err)
		}
	}
	seed("wf-bob-new", "bob", "verify", jobstore.WorkflowRunning, now+3)
	seed("wf-alice-new", "alice", "verify", jobstore.WorkflowFailed, now+2)
	seed("wf-alice-old", "alice", "verify", jobstore.WorkflowDone, now+1)
	seed("wf-alice-direct", "alice", "", jobstore.WorkflowDone, now+4)

	get := func(token string) workflow.WorkflowTemplate {
		resp := do(t, s, http.MethodGet, "/v1/workflow-templates", token, nil)
		var out struct {
			Templates []workflow.WorkflowTemplate `json:"templates"`
		}
		decode(t, resp, &out)
		for _, tpl := range out.Templates {
			if tpl.Name == "verify" {
				return tpl
			}
		}
		t.Fatal("verify template missing from the list")
		return workflow.WorkflowTemplate{}
	}
	alice := get("tok-alice")
	if alice.Runs != 2 || alice.LastRun == nil || alice.LastRun.ID != "wf-alice-new" || alice.LastRun.Status != jobstore.WorkflowFailed ||
		alice.LastRun.StartedAt != now+2 || alice.LastRun.EndedAt != now+7 {
		t.Fatalf("alice = %+v last %+v, want 2 runs, last wf-alice-new failed %d..%d", alice, alice.LastRun, now+2, now+7)
	}
	bob := get("tok-bob")
	if bob.Runs != 1 || bob.LastRun == nil || bob.LastRun.ID != "wf-bob-new" || bob.LastRun.EndedAt != 0 {
		t.Fatalf("bob = %+v last %+v, want 1 running run without an end time", bob, bob.LastRun)
	}
	// Another caller's run of a different template changes nothing for alice.
	seed("wf-carol-compare", "carol", "compare", jobstore.WorkflowDone, now)
	if alice := get("tok-alice"); alice.Runs != 2 {
		t.Fatalf("alice verify runs = %d after carol's compare run, want 2", alice.Runs)
	}
}

// TestWorkflowTemplateRunsCeiling pins that a project-ceiling caller's counts include only
// the runs inside its projects.
func TestWorkflowTemplateRunsCeiling(t *testing.T) {
	t.Parallel()
	s := newCeilingServer(t)
	grantAll(s)
	meta := s.jobs.Meta()
	now := time.Now().Unix()
	for i, project := range []string{"p1", "p2", "p2"} {
		if err := meta.InsertWorkflow(jobstore.Workflow{
			ID: fmt.Sprintf("wf-%s-%d", project, i), Status: jobstore.WorkflowDone, CallerID: "bob", CurrentStep: 1, TotalSteps: 1,
			SpecJSON: fmt.Sprintf(`{"steps":[{"project_key":%q,"agent":"exec"}]}`, project),
			Template: "verify", TemplateSource: "builtin", CreatedAt: now + int64(i), UpdatedAt: now + int64(i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	resp := do(t, s, http.MethodGet, "/v1/workflow-templates", ceilTokLim, nil)
	var out struct {
		Templates []workflow.WorkflowTemplate `json:"templates"`
	}
	decode(t, resp, &out)
	for _, tpl := range out.Templates {
		if tpl.Name == "verify" {
			if tpl.Runs != 1 || tpl.LastRun == nil || tpl.LastRun.ID != "wf-p1-0" {
				t.Fatalf("ceiling view = %+v last %+v, want only the p1 run", tpl, tpl.LastRun)
			}
			return
		}
	}
	t.Fatal("verify template missing from the list")
}
