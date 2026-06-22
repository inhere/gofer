package commands

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWorkflowSubcommandsRegistered asserts the P3-a `workflow` group is wired
// into the app with its run/show/list/cancel subcommands (and reachable via the
// `wf` alias).
func TestWorkflowSubcommandsRegistered(t *testing.T) {
	app := NewApp("test")

	wfCmd := app.GetCommand("workflow")
	if wfCmd == nil {
		t.Fatal("workflow command not registered")
	}
	for _, sub := range []string{"run", "show", "list", "cancel", "export", "events"} {
		if wfCmd.GetCommand(sub) == nil {
			t.Fatalf("workflow subcommand %q not registered", sub)
		}
	}
	// The `wf` alias resolves to the workflow command group.
	if !app.IsAlias("wf") || app.ResolveAlias("wf") != "workflow" {
		t.Fatalf("`wf` alias should resolve to workflow, got %q", app.ResolveAlias("wf"))
	}
	// `add` is an alias for `run` (so `wf add file` == `wf run file`): gcli stores
	// subcommand aliases on the group and resolves them at run time, so assert via
	// IsAlias/ResolveAlias (GetCommand is name-only, not alias-aware).
	if !wfCmd.IsAlias("add") || wfCmd.ResolveAlias("add") != "run" {
		t.Fatalf("`add` should be an alias for the run subcommand, got %q", wfCmd.ResolveAlias("add"))
	}
}

// TestParseWorkflowFile pins yaml -> WorkflowSpec decoding against the design §9
// shape: title + steps[] with project_key/agent/runner/prompt/cmd/cwd/timeout_sec/
// tags. It writes a temp file and asserts the StepSpec yaml tags bind correctly.
func TestParseWorkflowFile(t *testing.T) {
	yaml := `title: gen-test-review
steps:
  - name: gen
    project_key: my-proj
    agent: codex
    runner: local
    prompt: implement X
    tags: [ci, gen]
  - name: test
    project_key: my-proj
    agent: exec
    runner: local
    cmd: [bash, -c, "go test ./..."]
    cwd: sub
    timeout_sec: 120
`
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write wf file: %v", err)
	}

	spec, err := parseWorkflowFile(path)
	if err != nil {
		t.Fatalf("parseWorkflowFile: %v", err)
	}
	if spec.Title != "gen-test-review" {
		t.Fatalf("title=%q want gen-test-review", spec.Title)
	}
	if len(spec.Steps) != 2 {
		t.Fatalf("got %d steps want 2", len(spec.Steps))
	}

	s1 := spec.Steps[0]
	if s1.Name != "gen" || s1.ProjectKey != "my-proj" || s1.Agent != "codex" || s1.Runner != "local" {
		t.Fatalf("step1 core fields wrong: %+v", s1)
	}
	if s1.Prompt != "implement X" {
		t.Fatalf("step1 prompt=%q", s1.Prompt)
	}
	if len(s1.Tags) != 2 || s1.Tags[0] != "ci" || s1.Tags[1] != "gen" {
		t.Fatalf("step1 tags=%v", s1.Tags)
	}

	s2 := spec.Steps[1]
	if s2.Name != "test" || s2.Agent != "exec" {
		t.Fatalf("step2 core fields wrong: %+v", s2)
	}
	if len(s2.Cmd) != 3 || s2.Cmd[0] != "bash" || s2.Cmd[2] != "go test ./..." {
		t.Fatalf("step2 cmd=%v", s2.Cmd)
	}
	if s2.Cwd != "sub" || s2.TimeoutSec != 120 {
		t.Fatalf("step2 cwd/timeout wrong: cwd=%q timeout=%d", s2.Cwd, s2.TimeoutSec)
	}
}

// TestParseWorkflowFileNoSteps rejects an empty/stepless workflow file so `run`
// fails before hitting the server.
func TestParseWorkflowFileNoSteps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(path, []byte("title: empty\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := parseWorkflowFile(path); err == nil {
		t.Fatal("expected error for a workflow file with no steps")
	}
}

// TestWorkflowExitCodeMapping pins the --watch exit-code mapping: done=0,
// cancelled=130, failed/other=1 (mirrors job watch).
func TestWorkflowExitCodeMapping(t *testing.T) {
	cases := map[string]int{
		"done":      0,
		"cancelled": 130,
		"failed":    1,
		"running":   1,
		"":          1,
	}
	for status, want := range cases {
		if got := workflowExitCode(status); got != want {
			t.Fatalf("workflowExitCode(%q)=%d want %d", status, got, want)
		}
	}
}
