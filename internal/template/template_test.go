package template

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile writes one file, creating its parent dirs (test fixtures only).
func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// TestParseFrontmatterWhitelist: a template's frontmatter may only carry the job
// defaults the design whitelists (plus its own description and variable
// declarations) — the file is a task book, not a way to smuggle submit fields
// (caller / plan_id / cmd) past the server's own resolution and validation.
func TestParseFrontmatterWhitelist(t *testing.T) {
	tpl, err := Parse([]byte(`---
desc: 实施一个批次
agent: omp
runner: worker
timeout_sec: 3600
tags: [impl, batch]
verify: [go, test, ./...]
verify_timeout_sec: 900
review: true
read_only: true
worktree: true
fallback_agents: [claude]
vars:
  tasks:
    required: true
    desc: 任务正文
  base:
    default: main
---
# 任务

{{tasks}} @ {{base}}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if tpl.Desc != "实施一个批次" {
		t.Fatalf("desc = %q, want 实施一个批次", tpl.Desc)
	}
	m := tpl.Meta
	if m.Agent != "omp" || m.Runner != "worker" || m.TimeoutSec != 3600 {
		t.Fatalf("meta agent/runner/timeout = %q/%q/%d", m.Agent, m.Runner, m.TimeoutSec)
	}
	if strings.Join(m.Tags, ",") != "impl,batch" {
		t.Fatalf("meta tags = %v, want [impl batch]", m.Tags)
	}
	if strings.Join(m.Verify, " ") != "go test ./..." {
		t.Fatalf("meta verify = %v, want [go test ./...]", m.Verify)
	}
	if m.VerifyTimeoutSec != 900 || !m.Review || !m.ReadOnly || !m.Worktree {
		t.Fatalf("meta verify_timeout/review/read_only/worktree = %d/%v/%v/%v", m.VerifyTimeoutSec, m.Review, m.ReadOnly, m.Worktree)
	}
	if strings.Join(m.FallbackAgents, ",") != "claude" {
		t.Fatalf("meta fallback_agents = %v, want [claude]", m.FallbackAgents)
	}
	if v := tpl.Vars["tasks"]; !v.Required || v.Desc != "任务正文" {
		t.Fatalf("vars.tasks = %+v, want required with a desc", v)
	}
	if v := tpl.Vars["base"]; v.Default != "main" || v.Required {
		t.Fatalf("vars.base = %+v, want default main and not required", v)
	}
	if !strings.Contains(tpl.Body, "{{tasks}} @ {{base}}") {
		t.Fatalf("body = %q, want the prose after the frontmatter", tpl.Body)
	}
	if strings.Contains(tpl.Body, "agent:") {
		t.Fatalf("frontmatter leaked into the body: %q", tpl.Body)
	}

	// 白名单之外一律报错：一个任务书不该能塞进 caller/plan/cmd 这类字段。
	for _, bad := range []string{
		"plan_id: p1",
		"caller_id: someone",
		"cmd: [echo, hi]",
		"project_key: other",
		"vars:\n  tasks:\n    oops: 1",
	} {
		if _, err := Parse([]byte("---\n" + bad + "\n---\n正文\n")); err == nil {
			t.Errorf("Parse accepted the non-whitelisted key %q", bad)
		}
	}

	// 正文没有 frontmatter 时整篇都是正文（include 的片段就是这样）。
	plain, err := Parse([]byte("# 通用约束\n\n不要 push。\n"))
	if err != nil {
		t.Fatalf("Parse (no frontmatter): %v", err)
	}
	if plain.Desc != "" || plain.Body != "# 通用约束\n\n不要 push。\n" {
		t.Fatalf("plain template parsed as desc=%q body=%q", plain.Desc, plain.Body)
	}
}

// TestRenderVarsDefaultsRequiredAndBuiltins: the render resolves declared vars
// (given value > declared default), reports a REQUIRED var with no value instead of
// rendering it as empty prose, substitutes the builtins the server knows, and keeps
// an undeclared {{x}} verbatim with a warning — a typo must not silently erase text.
func TestRenderVarsDefaultsRequiredAndBuiltins(t *testing.T) {
	tpl := Template{
		Vars: map[string]Var{
			"tasks": {Required: true, Desc: "任务正文"},
			"base":  {Default: "main"},
		},
		Body: "base={{base}} tasks={{tasks}} project={{project}} cwd={{cwd}} date={{date}} head={{head}}\nkeep {{nope}}",
	}
	builtins := map[string]string{"project": "self", "cwd": ".", "date": "2026-09-18"}

	got, err := Render(tpl, map[string]string{"tasks": "T"}, builtins)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "base=main tasks=T project=self cwd=. date=2026-09-18 head=\nkeep {{nope}}"
	if got.Prompt != want {
		t.Fatalf("prompt = %q, want %q", got.Prompt, want)
	}
	if len(got.Missing) != 0 {
		t.Fatalf("missing = %v, want none (the default covers base, tasks was given)", got.Missing)
	}
	warns := strings.Join(got.Warnings, "\n")
	if !strings.Contains(warns, "head") {
		t.Fatalf("warnings = %q, want one about the unresolvable {{head}}", warns)
	}
	if !strings.Contains(warns, "nope") {
		t.Fatalf("warnings = %q, want one about the undeclared {{nope}}", warns)
	}

	// 调用方给了值的占位符即使模板没声明也替换（提交者就是在给眼前这份任务书定值），
	// 没人给值的才原样保留 + warn。
	got, err = Render(tpl, map[string]string{"tasks": "T", "extra": "E"}, builtins)
	if err != nil {
		t.Fatalf("Render (supplied but undeclared): %v", err)
	}
	if !strings.HasPrefix(got.Prompt, "base=main tasks=T ") {
		t.Fatalf("prompt = %q, want the supplied values substituted", got.Prompt)
	}
	extra := Template{Vars: nil, Body: "[{{extra}}]"}
	got, err = Render(extra, map[string]string{"extra": "E"}, nil)
	if err != nil {
		t.Fatalf("Render (undeclared only): %v", err)
	}
	if got.Prompt != "[E]" || len(got.Warnings) != 0 {
		t.Fatalf("prompt/warnings = %q/%v, want the supplied value substituted", got.Prompt, got.Warnings)
	}

	// 一个给了值的变量压过默认值；缺必填只是被"报出来"，其余正文照常渲染。
	got, err = Render(tpl, map[string]string{"base": "dev"}, builtins)
	if err != nil {
		t.Fatalf("Render (missing required): %v", err)
	}
	if len(got.Missing) != 1 || got.Missing[0] != "tasks" {
		t.Fatalf("missing = %v, want [tasks]", got.Missing)
	}
	if !strings.HasPrefix(got.Prompt, "base=dev tasks= ") {
		t.Fatalf("prompt = %q, want the given base and an empty required slot", got.Prompt)
	}

	// 有默认值的必填变量不算缺（默认值就是它的值）。
	got, err = Render(Template{Vars: map[string]Var{"x": {Required: true, Default: "d"}}, Body: "{{x}}"}, nil, nil)
	if err != nil {
		t.Fatalf("Render (required with default): %v", err)
	}
	if len(got.Missing) != 0 || got.Prompt != "d" {
		t.Fatalf("prompt/missing = %q/%v, want \"d\"/none", got.Prompt, got.Missing)
	}
}

// TestRenderIncludeOneLevelSameDirOnly: {{include: part.md}} splices a sibling file
// into the body — one level deep from the SAME directory only. A nested include stays
// verbatim (an include chain is how a template turns into an unbounded read), and any
// token that is not a bare file name (a path, an absolute path, an empty name) is
// refused rather than resolved against the filesystem.
func TestRenderIncludeOneLevelSameDirOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "part.md"), "P\n{{include: deep.md}}\n")
	writeFile(t, filepath.Join(dir, "deep.md"), "DEEP\n")
	main := filepath.Join(dir, "main.md")
	writeFile(t, main, "A\n{{include: part.md}}\nB\n")

	tpl, err := ParseFile(main)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	got, err := Render(tpl, nil, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got.Prompt, "P\n") || !strings.HasPrefix(got.Prompt, "A\n") {
		t.Fatalf("prompt = %q, want the sibling file spliced in place", got.Prompt)
	}
	if strings.Contains(got.Prompt, "DEEP") {
		t.Fatalf("a second-level include must not be expanded: %q", got.Prompt)
	}
	if !strings.Contains(got.Prompt, "{{include: deep.md}}") {
		t.Fatalf("a nested include must stay verbatim: %q", got.Prompt)
	}
	if !strings.Contains(strings.Join(got.Warnings, "\n"), "deep.md") {
		t.Fatalf("warnings = %v, want one about the nested include", got.Warnings)
	}

	// 逃逸/非法 token：一律报错，绝不拿它去读文件系统。
	for _, bad := range []string{
		"{{include: ../escape.md}}",
		"{{include: /etc/passwd}}",
		"{{include: sub/part.md}}",
		"{{include: }}",
	} {
		if _, err := Render(Template{Path: main, Body: "X " + bad + " Y"}, nil, nil); err == nil {
			t.Errorf("Render accepted the include token %q", bad)
		}
	}
	// 被 include 的文件不存在同样是错误：宁可失败，也不能悄悄少一段正文。
	if _, err := Render(Template{Path: main, Body: "{{include: absent.md}}"}, nil, nil); err == nil {
		t.Error("Render accepted a missing include")
	}
	// 从字节解析出来的模板没有目录，include 无从谈起。
	if _, err := Render(Template{Body: "{{include: part.md}}"}, nil, nil); err == nil {
		t.Error("Render accepted an include on a template that has no path")
	}
}

// TestResolveProjectBeforeGlobal: the project directory wins over the global one for
// a name both define; a project-less caller (worker-only project) still gets the
// global template; an unknown name and a name that is not a bare file name are the
// two distinct failures the callers map to 404 / 400.
func TestResolveProjectBeforeGlobal(t *testing.T) {
	project, global := t.TempDir(), t.TempDir()
	writeFile(t, ProjectPath(project, "dup"), "---\ndesc: 项目版\n---\nproject body\n")
	writeFile(t, GlobalPath(global, "dup"), "---\ndesc: 全局版\n---\nglobal body\n")
	writeFile(t, GlobalPath(global, "only-global"), "---\ndesc: 只有全局\n---\nglobal only\n")

	got, err := Resolve("dup", project, global)
	if err != nil {
		t.Fatalf("Resolve(dup): %v", err)
	}
	if got.Source != SourceProject || got.Desc != "项目版" {
		t.Fatalf("dup resolved to source=%q desc=%q, want the project copy", got.Source, got.Desc)
	}
	if got.Path != ProjectPath(project, "dup") {
		t.Fatalf("dup path = %q, want %q", got.Path, ProjectPath(project, "dup"))
	}
	if !strings.Contains(got.Body, "project body") {
		t.Fatalf("dup body = %q, want the project file's", got.Body)
	}

	got, err = Resolve("only-global", project, global)
	if err != nil {
		t.Fatalf("Resolve(only-global): %v", err)
	}
	if got.Source != SourceGlobal || got.Path != GlobalPath(global, "only-global") {
		t.Fatalf("only-global resolved to source=%q path=%q", got.Source, got.Path)
	}

	// 项目目录不可读（worker-only 项目在 host 上就是占位）：只剩全局目录可用。
	if _, err := Resolve("dup", "", global); err != nil {
		t.Fatalf("Resolve without a project dir: %v", err)
	}

	if _, err := Resolve("absent", project, global); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve(absent) error = %v, want ErrNotFound", err)
	}
	for _, bad := range []string{"", "../escape", "a/b", ".", "..", "sub\\x", "with space"} {
		if _, err := Resolve(bad, project, global); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Resolve(%q) error = %v, want ErrInvalidName", bad, err)
		}
	}
}

// TestListTemplates: the listing shows both sources with the project copy shadowing
// the global one of the same name, sorted by name, and a template that fails to parse
// is REPORTED (with its error) rather than hidden — that is exactly the file a reader
// is looking for when the submit fails.
func TestListTemplates(t *testing.T) {
	project, global := t.TempDir(), t.TempDir()
	writeFile(t, ProjectPath(project, "impl-batch"), "---\ndesc: 批次实施\nvars:\n  tasks: {required: true}\n---\nbody\n")
	writeFile(t, GlobalPath(global, "impl-batch"), "---\ndesc: 全局版\n---\nbody\n")
	writeFile(t, GlobalPath(global, "common"), "---\ndesc: 通用约束\n---\nbody\n")
	writeFile(t, ProjectPath(project, "broken"), "---\nplan_id: nope\n---\nbody\n")

	list := List(project, global)
	if len(list) != 3 {
		t.Fatalf("List returned %d entries, want 3: %+v", len(list), list)
	}
	byName := make(map[string]Info, len(list))
	names := make([]string, 0, len(list))
	for _, in := range list {
		byName[in.Name] = in
		names = append(names, in.Name)
	}
	if strings.Join(names, ",") != "broken,common,impl-batch" {
		t.Fatalf("List order = %v, want sorted by name", names)
	}
	if got := byName["impl-batch"]; got.Source != SourceProject || got.Desc != "批次实施" {
		t.Fatalf("impl-batch = %+v, want the project copy with its desc", got)
	}
	if !byName["impl-batch"].Vars["tasks"].Required {
		t.Fatalf("impl-batch vars = %+v, want the declared required tasks", byName["impl-batch"].Vars)
	}
	if got := byName["common"]; got.Source != SourceGlobal || got.Desc != "通用约束" {
		t.Fatalf("common = %+v, want the global copy", got)
	}
	if byName["broken"].Err == "" {
		t.Fatalf("broken = %+v, want the parse error reported", byName["broken"])
	}

	// 两个目录都不存在时列表为空（而不是报错）：一台机器可以一个模板都没有。
	if got := List(filepath.Join(project, "nope"), ""); len(got) != 0 {
		t.Fatalf("List of missing dirs = %+v, want empty", got)
	}
}
