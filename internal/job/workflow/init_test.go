package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "github.com/goccy/go-yaml"

	"github.com/inhere/gofer/internal/workflowstep"
)

// writeTestFile writes one file of a fake repository (creating parents).
func writeTestFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// parseInitSpec renders the starter of kind for dir and asserts what `wf init` promises
// about every generated file: it parses as a workflow spec, it passes the same template
// validation a run / a template save applies, and it has exactly one step.
func parseInitSpec(t *testing.T, kind, dir string) (string, Spec) {
	t.Helper()
	content, err := GenerateWorkflowInit(kind, dir)
	if err != nil {
		t.Fatalf("GenerateWorkflowInit(%s): %v", kind, err)
	}
	var spec Spec
	if err := yaml.Unmarshal([]byte(content), &spec); err != nil {
		t.Fatalf("generated %s file does not parse: %v\n%s", kind, err, content)
	}
	if _, err := ValidateWorkflowTemplateContent(content); err != nil {
		t.Fatalf("generated %s file does not validate: %v\n%s", kind, err, content)
	}
	if len(spec.Steps) == 0 {
		t.Fatalf("generated %s file has no steps:\n%s", kind, content)
	}
	return content, spec
}

// fanCommands lists the cmd of every fan of step 1 as a space-joined line, which is how
// the tests below compare the pre-filled checks.
func fanCommands(spec Spec) []string {
	out := make([]string, 0, len(spec.Steps[0].Fan))
	for _, f := range spec.Steps[0].Fan {
		out = append(out, strings.Join(f.Cmd, " "))
	}
	return out
}

func hasFan(have []string, want string) bool {
	for _, h := range have {
		if h == want {
			return true
		}
	}
	return false
}

func TestGenerateVerifyInitGoRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/x\n\ngo 1.25\n")

	content, spec := parseInitSpec(t, InitVerify, dir)
	step := spec.Steps[0]
	if step.Name != "checks" || step.Agent != "exec" || step.Join != "all" || step.Ref == "" {
		t.Fatalf("step = %+v, want an exec checks step with ref + join: all", step)
	}
	cmds := fanCommands(spec)
	if !hasFan(cmds, "go test ./...") || !hasFan(cmds, "go vet ./...") {
		t.Fatalf("fans = %v, want go test ./... and go vet ./...", cmds)
	}
	// The starter has to teach the common variations without a doc lookup.
	for _, want := range []string{"-race", "GOOS: darwin", "runner:"} {
		if !strings.Contains(content, want) {
			t.Fatalf("generated verify file does not mention %q:\n%s", want, content)
		}
	}
	if !strings.Contains(content, "提交到仓库后 server 才能读到") {
		t.Fatalf("generated verify file is missing the commit reminder:\n%s", content)
	}
}

func TestGenerateVerifyInitNodeRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, dir, "package.json", `{"scripts": {"test": "vitest", "build": "vite build", "lint": "eslint .", "dev": "vite"}}`)

	_, spec := parseInitSpec(t, InitVerify, dir)
	cmds := fanCommands(spec)
	for _, want := range []string{"npm run test", "npm run build", "npm run lint"} {
		if !hasFan(cmds, want) {
			t.Fatalf("fans = %v, want %q", cmds, want)
		}
	}
	if hasFan(cmds, "npm run dev") {
		t.Fatalf("fans = %v, dev is not a check and must not be fanned out", cmds)
	}
}

func TestGenerateVerifyInitNodeSubdir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, dir, "web/package.json", `{"scripts": {"test": "vitest run", "build": "vite build"}}`)
	writeTestFile(t, dir, "web/pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	writeTestFile(t, dir, "web/node_modules/ignored/package.json", `{"scripts": {"test": "ignored"}}`)

	_, spec := parseInitSpec(t, InitVerify, dir)
	cmds := fanCommands(spec)
	if !hasFan(cmds, "pnpm run test") || !hasFan(cmds, "pnpm run build") {
		t.Fatalf("fans = %v, want pnpm run test / build (pnpm-lock.yaml)", cmds)
	}
	for _, f := range spec.Steps[0].Fan {
		if f.Cwd != "web" {
			t.Fatalf("fan %+v: cwd = %q, want web", f, f.Cwd)
		}
		if f.Name == "" || strings.Contains(f.Name, "/") {
			t.Fatalf("fan name %q must be a single identifier (cwd is not part of it)", f.Name)
		}
	}
	if hasFan(cmds, "pnpm run ignored") {
		t.Fatalf("fans = %v, a node_modules package.json must not be scanned", cmds)
	}
}

func TestGenerateVerifyInitMixedRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/x\n\ngo 1.25\n")
	writeTestFile(t, dir, "package.json", `{"scripts": {"test": "vitest run"}}`)

	_, spec := parseInitSpec(t, InitVerify, dir)
	cmds := fanCommands(spec)
	for _, want := range []string{"go test ./...", "go vet ./...", "npm run test"} {
		if !hasFan(cmds, want) {
			t.Fatalf("fans = %v, want %q (Go and node checks share the one step)", cmds, want)
		}
	}
	if n := len(spec.Steps); n != 1 {
		t.Fatalf("steps = %d, want 1 (parallel checks are fans of one step)", n)
	}
}

func TestGenerateVerifyInitEmptyRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content, spec := parseInitSpec(t, InitVerify, dir)
	step := spec.Steps[0]
	if step.Join != "" || len(step.Fan) != 0 {
		t.Fatalf("empty repository got join/fan (%+v); one placeholder step is enough", step)
	}
	if len(step.Cmd) == 0 {
		t.Fatalf("empty repository got no placeholder command: %+v", step)
	}
	if !strings.Contains(content, "没有识别到 go.mod / package.json") {
		t.Fatalf("empty repository file does not explain how to change it:\n%s", content)
	}
}

func TestWriteWorkflowInit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/x\n")

	path, err := WriteWorkflowInit(InitVerify, dir, false)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if want := filepath.Join(dir, ".gofer", "workflows", "verify.yaml"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := GenerateWorkflowInit(InitVerify, dir); err != nil {
		t.Fatalf("regenerate: %v", err)
	}

	// An operator's edits must survive a second `wf init` unless --force is given.
	writeTestFile(t, dir, ".gofer/workflows/verify.yaml", "edited: true\n")
	if _, err := WriteWorkflowInit(InitVerify, dir, false); !errors.Is(err, ErrWorkflowFileExists) {
		t.Fatalf("second write err = %v, want ErrWorkflowFileExists", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".gofer", "workflows", "verify.yaml"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(body) != "edited: true\n" {
		t.Fatalf("file was overwritten without --force: %q", body)
	}

	if _, err := WriteWorkflowInit(InitVerify, dir, true); err != nil {
		t.Fatalf("forced write: %v", err)
	}
	body, err = os.ReadFile(filepath.Join(dir, ".gofer", "workflows", "verify.yaml"))
	if err != nil {
		t.Fatalf("read back after force: %v", err)
	}
	if strings.Contains(string(body), "edited: true") {
		t.Fatalf("--force did not replace the file:\n%s", body)
	}
}

func TestGenerateInitRejectsUnknownKind(t *testing.T) {
	t.Parallel()
	if _, err := GenerateWorkflowInit("deploy", t.TempDir()); err == nil {
		t.Fatal("unknown kind was accepted")
	}
}

func TestGenerateReleaseInit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, dir, ".github/workflows/unit-tests.yml", "name: Unit-Tests\non: push\njobs: {}\n")
	writeTestFile(t, dir, ".github/workflows/lint.yml", "name: Lint\non: push\njobs: {}\n")

	content, spec := parseInitSpec(t, InitRelease, dir)
	if len(spec.Steps) < 2 {
		t.Fatalf("steps = %d, want push + wait-ci", len(spec.Steps))
	}
	push, ci := spec.Steps[0], spec.Steps[1]
	if push.Uses != "gofer/push" || push.OnFailure != onFailureRetry {
		t.Fatalf("step 1 = %+v, want gofer/push with on_failure: retry", push)
	}
	refs, ok := push.With["refs"].([]any)
	if !ok || len(refs) != 2 || refs[1] != "v${vars.version}" {
		t.Fatalf("push refs = %v, want [<branch> v${vars.version}]", push.With["refs"])
	}
	if ci.Uses != "gofer/wait-ci" {
		t.Fatalf("step 2 = %+v, want gofer/wait-ci", ci)
	}
	required, ok := ci.With["required"].([]any)
	if !ok || len(required) != 2 {
		t.Fatalf("wait-ci required = %v, want the .github/workflows names", ci.With["required"])
	}
	// File-name order of .github/workflows: lint.yml before unit-tests.yml.
	if required[0] != "Lint" || required[1] != "Unit-Tests" {
		t.Fatalf("wait-ci required = %v, want [Lint Unit-Tests]", required)
	}
	if !strings.Contains(content, "wait-assets") {
		t.Fatalf("release file should keep a wait-assets example:\n%s", content)
	}
}

func TestGenerateReleaseInitNoWorkflows(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content, spec := parseInitSpec(t, InitRelease, dir)
	if _, ok := spec.Steps[1].With["required"]; ok {
		t.Fatalf("required was pre-filled without any .github/workflows: %v", spec.Steps[1].With)
	}
	if !strings.Contains(content, "# required:") {
		t.Fatalf("release file should show the commented required example:\n%s", content)
	}
}

func TestReadGitHubWorkflowNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, dir, "a.yml", "name: Alpha\n")
	writeTestFile(t, dir, "b.yaml", "on: push\njobs: {}\n") // no name: -> skipped
	writeTestFile(t, dir, "c.json", "{\"name\": \"not-a-workflow\"}")
	writeTestFile(t, dir, "sub/d.yml", "name: Nested\n")

	got := readGitHubWorkflows(dir)
	if len(got) != 1 || got[0] != "Alpha" {
		t.Fatalf("names = %v, want [Alpha] (only top-level .yml/.yaml with a name:)", got)
	}
}

func TestBuiltinVerifyReleaseTemplates(t *testing.T) {
	t.Parallel()
	verify, err := ResolveBuiltinWorkflowTemplate("verify", map[string]string{
		"project": "demo", "commit": "abc123", "cmd": "go test ./...",
	})
	if err != nil {
		t.Fatalf("resolve verify: %v", err)
	}
	if len(verify.Steps) != 1 {
		t.Fatalf("verify steps = %d, want 1", len(verify.Steps))
	}
	step := verify.Steps[0]
	if step.ProjectKey != "demo" || step.Agent != "exec" || step.Ref != "abc123" {
		t.Fatalf("verify step = %+v, want project/exec/ref filled from the variables", step)
	}
	if step.Run != "go test ./..." || len(step.Cmd) != 0 {
		t.Fatalf("verify run = %q cmd = %v, want the run: script spelling (converted to cmd at submit)", step.Run, step.Cmd)
	}

	release, err := ResolveBuiltinWorkflowTemplate("release", map[string]string{
		"project": "demo", "version": "1.2.3",
	})
	if err != nil {
		t.Fatalf("resolve release: %v", err)
	}
	if release.Title != "release v1.2.3" {
		t.Fatalf("release title = %q, want the version rendered", release.Title)
	}
	if len(release.Steps) != 2 || release.Steps[0].Uses != "gofer/push" || release.Steps[1].Uses != "gofer/wait-ci" {
		t.Fatalf("release steps = %+v, want push -> wait-ci", release.Steps)
	}
	refs, ok := release.Steps[0].With["refs"].([]any)
	if !ok || len(refs) != 2 || refs[0] != "main" || refs[1] != "v1.2.3" {
		t.Fatalf("release refs = %v, want [main v1.2.3] (branch defaults to main)", release.Steps[0].With["refs"])
	}
	if err := validateRetry(release); err != nil {
		t.Fatalf("release retry: %v", err)
	}
	for _, st := range release.Steps {
		if err := workflowstep.Validate(st.Uses, st.With); err != nil {
			t.Fatalf("release step %q: %v", st.Name, err)
		}
	}
}
