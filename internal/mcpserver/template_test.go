package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/template"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// writeMCPTemplate writes one project template (<project>/.gofer/templates/<name>.md).
func writeMCPTemplate(t *testing.T, root, name, src string) {
	t.Helper()
	path := template.ProjectPath(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// TestRunJobTemplateParams: gofer_run_job exposes template/vars (an agent can discover
// the capability from the schema), the server renders the task book into the job's
// prompt and applies its defaults, and request_json keeps the rendered prompt plus the
// template/vars it came from. An unknown template fails the tool call instead of
// submitting an empty-prompt job.
func TestRunJobTemplateParams(t *testing.T) {
	session, jobs := connect(t)
	t.Setenv(config.EnvConfigDir, t.TempDir())
	root := jobs.Config().Projects["self"].HostPath
	writeMCPTemplate(t, root, "t1", "---\ndesc: 冒烟\nagent: exec\ntags: [tpl]\n---\n运行 {{what}}\n")

	schema := runJobSchema(t, session)
	for _, key := range []string{"template", "vars"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Fatalf("gofer_run_job input schema has no %q", key)
		}
	}

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self",
			"agent":       "exec", // the tool schema requires an agent; the template fills the rest
			"runner":      "local",
			"cmd":         testcmd.Cmd(t, "exit", "0"),
			"cwd":         ".",
			"timeout_sec": 30,
			"template":    "t1",
			"vars":        map[string]string{"what": "测试"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)
	final, ok := jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("Wait: job %s not found", created.ID)
	}
	if final.Status != job.StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	var req job.JobRequest
	if err := json.Unmarshal([]byte(final.RequestJSON), &req); err != nil {
		t.Fatalf("request_json not valid JSON: %v", err)
	}
	if req.Prompt != "运行 测试" {
		t.Fatalf("request prompt = %q, want the rendered body", req.Prompt)
	}
	if len(req.Tags) != 1 || req.Tags[0] != "tpl" {
		t.Fatalf("request tags = %v, want the template's default", req.Tags)
	}
	if req.Template != "t1" || req.TemplateVars["what"] != "测试" {
		t.Fatalf("request template/vars = %q/%v, want them kept", req.Template, req.TemplateVars)
	}

	bad, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self", "agent": "exec", "runner": "local",
			"cmd": testcmd.Cmd(t, "exit", "0"), "cwd": ".", "timeout_sec": 30,
			"template": "absent",
		},
	})
	if err == nil && (bad == nil || !bad.IsError) {
		t.Fatalf("an unknown template must fail the tool call, got %+v", bad)
	}
}

// TestListTemplatesTool: gofer_list_templates reports the task books a project can be
// driven with (name / source / path / desc / vars) so an agent can pick one before
// submitting; an unknown project is an error, not an empty list.
func TestListTemplatesTool(t *testing.T) {
	session, jobs := connect(t)
	t.Setenv(config.EnvConfigDir, t.TempDir())
	root := jobs.Config().Projects["self"].HostPath
	writeMCPTemplate(t, root, "impl", "---\ndesc: 批次实施\nvars:\n  tasks: {required: true}\n---\nbody\n")

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_list_templates",
		Arguments: map[string]any{"project": "self"},
	})
	if err != nil {
		t.Fatalf("CallTool list_templates: %v", err)
	}
	var out templatesView
	structured(t, res, &out)
	if out.Project != "self" || len(out.Templates) != 1 {
		t.Fatalf("list_templates = %+v, want self with one template", out)
	}
	got := out.Templates[0]
	if got.Name != "impl" || got.Source != template.SourceProject || got.Desc != "批次实施" {
		t.Fatalf("template entry = %+v, want the project's impl with its desc", got)
	}
	if !got.Vars["tasks"].Required || !strings.HasSuffix(got.Path, "impl.md") {
		t.Fatalf("template entry = %+v, want the declared var and the source path", got)
	}

	bad, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_list_templates",
		Arguments: map[string]any{"project": "nope"},
	})
	if err == nil && (bad == nil || !bad.IsError) {
		t.Fatalf("an unknown project must fail the tool call, got %+v", bad)
	}
}
