package commands

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/job"
)

// TestJobRunTemplateFlags: `job run -t <name> --var k=v` puts the template name and
// the variables on the wire and leaves the RUNNER unset (the template's own runner
// default is the next authority); -t is mutually exclusive with -f and with a
// post-`--` argv, and a --var without a template is a usage error rather than a
// silently dropped value.
func TestJobRunTemplateFlags(t *testing.T) {
	jobRunOpts = jobRunFlags{}
	t.Cleanup(func() { jobRunOpts = jobRunFlags{} })

	app := NewApp("test")
	var got job.JobRequest
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		req, err := buildJobRunRequest(c, nil)
		if err != nil {
			return err
		}
		got = req
		return nil
	}

	if code := app.Run([]string{"job", "run", "-p", "self", "-t", "impl-batch",
		"--var", "tasks=做 A", "--var", "base=dev", "--prompt", "追加"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if got.Template != "impl-batch" {
		t.Fatalf("JobRequest.Template = %q, want impl-batch", got.Template)
	}
	if got.TemplateVars["tasks"] != "做 A" || got.TemplateVars["base"] != "dev" {
		t.Fatalf("JobRequest.TemplateVars = %v, want both --var values", got.TemplateVars)
	}
	if got.Prompt != "追加" {
		t.Fatalf("JobRequest.Prompt = %q, want the --prompt addendum", got.Prompt)
	}
	if got.Runner != "" {
		t.Fatalf("JobRequest.Runner = %q, want it left to the template/server", got.Runner)
	}
	if got.Agent != "" {
		t.Fatalf("JobRequest.Agent = %q, want it left to the template", got.Agent)
	}

	for _, bad := range [][]string{
		{"job", "run", "-p", "self", "-t", "impl", "-f", "task.md"},
		{"job", "run", "-p", "self", "-t", "impl", "--", "go", "version"},
		{"job", "run", "-p", "self", "-a", "exec", "--var", "tasks=x", "--", "go", "version"},
		{"job", "run", "-p", "self", "-t", "impl", "--var", "novalue"},
	} {
		jobRunOpts = jobRunFlags{}
		runCmd.Func = func(c *gcli.Command, _ []string) error {
			_, err := buildJobRunRequest(c, nil)
			return err
		}
		if code := app.Run(bad); code == 0 {
			t.Errorf("app.Run(%v) succeeded, want a usage error", bad)
		}
	}
}

// TestTemplateShowRenders: `template show <name> --var k=v` prints where the template
// came from, the variables it declares (required flagged) and the RENDERED body
// preview — the reader sees exactly the prompt a submit would send, before sending it.
// The wire is the read-only template endpoint (hand-written JSON here so a field
// rename on either side fails this test).
func TestTemplateShowRenders(t *testing.T) {
	bodyWithVars := `{
  "name": "impl",
  "source": "project",
  "path": "D:/proj/.gofer/templates/impl.md",
  "desc": "批次实施",
  "meta": {"agent": "omp", "timeout_sec": 600},
  "vars": {"tasks": {"required": true, "desc": "任务正文"}, "base": {"default": "main"}},
  "body": "实施 {{tasks}}（base={{base}}）",
  "render": {
    "prompt": "实施 做 A（base=main）",
    "warnings": ["undeclared {{extra}} kept as-is"]
  }
}`
	// 服务端渲染的预览：CLI 只是把 --var 传过去（这里断言它真的传了），渲染结果与
	// "缺必填变量"由服务端如实回答 —— stub 照这个契约回话。
	bodyNoVars := `{
  "name": "impl",
  "source": "project",
  "path": "D:/proj/.gofer/templates/impl.md",
  "desc": "批次实施",
  "vars": {"tasks": {"required": true, "desc": "任务正文"}, "base": {"default": "main"}},
  "body": "实施 {{tasks}}（base={{base}}）",
  "render": {"prompt": "实施 （base=main）", "missing": ["tasks"]}
}`
	var gotVars []string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/self/templates/impl" {
			http.NotFound(w, r)
			return
		}
		gotVars = r.URL.Query()["var"]
		out := bodyWithVars
		if len(gotVars) == 0 {
			out = bodyNoVars
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(out))
	})
	stubAgentServer(t, h)

	templateShowOpts.project = "self"
	templateShowOpts.vars = []string{"tasks=做 A"}
	t.Cleanup(func() { templateShowOpts = templateShowFlags{} })

	c := bindCmd(findSub(t, NewTemplateCmd(), "show"))
	out := captureOutput(t, func() {
		if err := runTemplateShow(c, []string{"impl"}); err != nil {
			t.Fatalf("template show: %v", err)
		}
	})

	for _, want := range []string{
		"D:/proj/.gofer/templates/impl.md", // 来源路径
		"批次实施",                             // 描述
		"tasks",                            // 变量表
		"(required)",
		"base", // 默认值
		"main",
		"实施 做 A（base=main）",    // 服务端渲染后的正文预览
		"undeclared {{extra}}", // 渲染告警一并显示
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("template show output missing %q:\n%s", want, out)
		}
	}
	if len(gotVars) != 1 || gotVars[0] != "tasks=做 A" {
		t.Fatalf("template show sent var=%v, want the --var value", gotVars)
	}

	// 缺必填变量时如实报告，而不是打印一段少了正文的预览。
	templateShowOpts.vars = nil
	err := runTemplateShow(c, []string{"impl"})
	if err == nil || !strings.Contains(err.Error(), "tasks") {
		t.Fatalf("template show without the required var: err = %v, want it to name tasks", err)
	}
}
