package job

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/template"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// newTemplateService builds a service whose "fake" cli-agent prints the prompt it
// received, with the project pointed at a temp dir so a test can drop templates into
// <project>/.gofer/templates/. GOFER_CONFIG_DIR is redirected to an empty dir so the
// GLOBAL template directory under test is the test's own, never the developer's.
func newTemplateService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv(config.EnvConfigDir, t.TempDir())
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"fake"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"fake": {
				Type:    agent.TypeCLIAgent,
				Command: testcmd.Path(t),
				Args:    []string{"printf", "{{prompt}}"},
			},
		},
	}
	return newServiceFromCfg(t, root, cfg), root
}

// writeTemplate writes one project template (<root>/.gofer/templates/<name>.md).
func writeTemplate(t *testing.T, root, name, src string) {
	t.Helper()
	path := template.ProjectPath(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// jobStdout reads one job's whole stdout log.
func jobStdout(t *testing.T, root, jobID string) string {
	t.Helper()
	out, err := store.NewFileStore(filepath.Join(root, "self")).ReadLogTail(jobID, store.StreamStdout, 0)
	if err != nil {
		t.Fatalf("read stdout of %s: %v", jobID, err)
	}
	return string(out)
}

// templateRequest reads back the request a job was submitted with (request_json),
// which is the thing a rerun replays.
func templateRequest(t *testing.T, res JobResult) JobRequest {
	t.Helper()
	var req JobRequest
	if err := json.Unmarshal([]byte(res.RequestJSON), &req); err != nil {
		t.Fatalf("request_json is not a JobRequest: %v", err)
	}
	return req
}

// TestSubmitWithTemplateRendersPromptAndDefaults: a submit that names a template runs
// the rendered task book — the agent and runner come from the template when the caller
// left them unset, the template's job defaults (timeout/tags/verify) land on the
// request, an explicit flag still beats them, and request_json stores the RENDERED
// prompt next to the template name and vars (what a rerun replays, auditably).
func TestSubmitWithTemplateRendersPromptAndDefaults(t *testing.T) {
	s, root := newTemplateService(t)
	verifyArgv := testcmd.Cmd(t, "exit", "0")
	src := "---\ndesc: 批次实施\nagent: fake\nrunner: local\ntimeout_sec: 1200\n" +
		"tags: [impl, batch]\nverify:\n"
	for _, a := range verifyArgv {
		src += "  - '" + a + "'\n"
	}
	src += "verify_timeout_sec: 99\n---\n实现 {{tasks}}（{{project}}）\n"
	writeTemplate(t, root, "impl-batch", src)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", // agent/runner deliberately left to the template
		Cwd:        ".", TimeoutSec: 0,
		Template:     "impl-batch",
		TemplateVars: map[string]string{"tasks": "T-1"},
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if out := jobStdout(t, root, final.ID); !strings.Contains(out, "实现 T-1（self）") {
		t.Fatalf("the agent did not receive the rendered prompt:\n%s", out)
	}
	// 模板里的 verify 真的跑了：只有跑过才会是 passed。
	if final.Verify == nil || final.Verify.Status != VerifyPassed {
		t.Fatalf("verify = %+v, want the template's step to have run and passed", final.Verify)
	}
	if final.TimeoutSec != 1200 {
		t.Fatalf("timeout = %d, want the template's 1200", final.TimeoutSec)
	}

	req := templateRequest(t, final)
	if req.Prompt != "实现 T-1（self）" {
		t.Fatalf("request prompt = %q, want the rendered body", req.Prompt)
	}
	if req.Agent != "fake" || req.Runner != "local" {
		t.Fatalf("request agent/runner = %q/%q, want the template's", req.Agent, req.Runner)
	}
	if strings.Join(req.Tags, ",") != "impl,batch" {
		t.Fatalf("request tags = %v, want the template's", req.Tags)
	}
	if req.VerifyTimeoutSec != 99 || len(req.Verify) != len(verifyArgv) {
		t.Fatalf("request verify = %v (%ds), want the template's argv and 99s", req.Verify, req.VerifyTimeoutSec)
	}
	if req.Template != "impl-batch" || req.TemplateVars["tasks"] != "T-1" {
		t.Fatalf("request template/vars = %q/%v, want them kept for audit", req.Template, req.TemplateVars)
	}

	// 显式旗标压过模板默认（--timeout 60 而非模板的 1200）。
	explicit := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "fake", Runner: "local", Cwd: ".", TimeoutSec: 60,
		Template: "impl-batch", TemplateVars: map[string]string{"tasks": "T-2"},
	})
	if explicit.TimeoutSec != 60 {
		t.Fatalf("timeout = %d, want the explicit 60 to win over the template default", explicit.TimeoutSec)
	}
}

// TestSubmitTemplateMissingVarRejected: a template's required variable with no value
// (and no default) rejects the submit as a bad request naming the missing var — never
// a job whose prompt silently lost a paragraph.
func TestSubmitTemplateMissingVarRejected(t *testing.T) {
	s, root := newTemplateService(t)
	writeTemplate(t, root, "impl", "---\nagent: fake\nvars:\n  tasks: {required: true, desc: 任务正文}\n---\n正文 {{tasks}}\n")

	_, err := s.Submit(JobRequest{ProjectKey: "self", Cwd: ".", TimeoutSec: 30, Template: "impl"})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "tasks") {
		t.Fatalf("error = %q, want it to name the missing var", err)
	}

	// 未知模板名与非法名字同样是提交期的 400（不是 500，也不是一个跑空 prompt 的 job）。
	for _, name := range []string{"absent", "../escape"} {
		if _, err := s.Submit(JobRequest{ProjectKey: "self", Cwd: ".", TimeoutSec: 30, Template: name}); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("Submit(template=%q) error = %v, want ErrInvalidRequest", name, err)
		}
	}
}

// TestSubmitTemplateAppendsPrompt: --prompt alongside a template is APPENDED to the
// rendered body (blank line between), in that order — the template is the task book
// and the flag is the run-specific addendum, and the persisted request holds the
// concatenation the agent actually read.
func TestSubmitTemplateAppendsPrompt(t *testing.T) {
	s, root := newTemplateService(t)
	writeTemplate(t, root, "impl", "---\nagent: fake\n---\n模板正文\n")

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Cwd: ".", TimeoutSec: 30,
		Template: "impl", Prompt: "追加正文",
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if req := templateRequest(t, final); req.Prompt != "模板正文\n\n追加正文" {
		t.Fatalf("request prompt = %q, want body + blank line + appended flag text", req.Prompt)
	}
	if out := jobStdout(t, root, final.ID); !strings.Contains(out, "模板正文") || !strings.Contains(out, "追加正文") {
		t.Fatalf("the agent did not receive both parts:\n%s", out)
	}
}
