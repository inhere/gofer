package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/template"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// writeTemplateFile writes one .gofer/templates file under a project root.
func writeTemplateFile(t *testing.T, root, name, src string) {
	t.Helper()
	path := template.ProjectPath(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// TestListProjectTemplates: the console and the CLI read a project's task-book
// templates over HTTP — both sources (project shadows global) with the declared vars
// — and an unknown project / unknown template are 404s rather than empty lists, so a
// typo cannot read as "no templates".
func TestListProjectTemplates(t *testing.T) {
	s := newTestServer(t, testToken, false)
	global := t.TempDir()
	t.Setenv(config.EnvConfigDir, global)
	root := s.jobs.Config().Projects["self"].HostPath
	writeTemplateFile(t, root, "impl-batch", "---\ndesc: 批次实施\nagent: exec\nvars:\n  tasks: {required: true}\n---\nbody\n")
	if err := os.MkdirAll(filepath.Join(global, "templates"), 0o755); err != nil {
		t.Fatalf("MkdirAll global templates: %v", err)
	}
	if err := os.WriteFile(filepath.Join(global, "templates", "common.md"), []byte("---\ndesc: 通用约束\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write global template: %v", err)
	}

	resp := do(t, s, http.MethodGet, "/v1/projects/self/templates", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d, want 200", resp.StatusCode)
	}
	var list struct {
		Templates []template.Info `json:"templates"`
	}
	decode(t, resp, &list)
	byName := map[string]template.Info{}
	for _, in := range list.Templates {
		byName[in.Name] = in
	}
	if got, ok := byName["impl-batch"]; !ok || got.Source != template.SourceProject || got.Desc != "批次实施" {
		t.Fatalf("impl-batch entry = %+v, want the project copy", got)
	}
	if !byName["impl-batch"].Vars["tasks"].Required {
		t.Fatalf("impl-batch vars = %+v, want the declared required var", byName["impl-batch"].Vars)
	}
	if got, ok := byName["common"]; !ok || got.Source != template.SourceGlobal {
		t.Fatalf("common entry = %+v, want the global copy", got)
	}

	detail := do(t, s, http.MethodGet, "/v1/projects/self/templates/impl-batch", testToken, nil)
	if detail.StatusCode != http.StatusOK {
		t.Fatalf("detail status=%d, want 200", detail.StatusCode)
	}
	var tpl template.Template
	decode(t, detail, &tpl)
	if tpl.Meta.Agent != "exec" || !strings.Contains(tpl.Body, "body") {
		t.Fatalf("detail = %+v, want the template's meta and body", tpl)
	}

	if resp := do(t, s, http.MethodGet, "/v1/projects/nope/templates", testToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown project status=%d, want 404", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodGet, "/v1/projects/self/templates/absent", testToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown template status=%d, want 404", resp.StatusCode)
	}
}

// TestSubmitJobWithTemplate: POST /v1/jobs with template/vars renders the task book
// server-side — the job runs with the template's agent and the rendered prompt, and
// the persisted request keeps the rendered prompt plus the template/vars it came from.
func TestSubmitJobWithTemplate(t *testing.T) {
	s := newTestServer(t, testToken, false)
	t.Setenv(config.EnvConfigDir, t.TempDir())
	root := s.jobs.Config().Projects["self"].HostPath
	writeTemplateFile(t, root, "t1", "---\ndesc: 冒烟\nagent: exec\ntags: [tpl]\n---\n运行 {{what}}\n")

	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30,
		Template: "t1", TemplateVars: map[string]string{"what": "测试"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	final := waitDone(t, s, created.ID)
	if final.Status != job.StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}

	rr := do(t, s, http.MethodGet, "/v1/jobs/"+created.ID+"/request", testToken, nil)
	if rr.StatusCode != http.StatusOK {
		t.Fatalf("get request status=%d, want 200", rr.StatusCode)
	}
	var got job.JobRequest
	decode(t, rr, &got)
	if got.Prompt != "运行 测试" {
		t.Fatalf("request prompt = %q, want the rendered body", got.Prompt)
	}
	if got.Template != "t1" || got.TemplateVars["what"] != "测试" {
		t.Fatalf("request template/vars = %q/%v, want them kept", got.Template, got.TemplateVars)
	}
	if strings.Join(got.Tags, ",") != "tpl" {
		t.Fatalf("request tags = %v, want the template's", got.Tags)
	}

	// 缺必填变量 / 未知模板都是 400（提交期拒绝，而不是一个跑空 prompt 的 job）。
	bad := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30, Template: "absent",
	})
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown template status=%d, want 400", bad.StatusCode)
	}
}
