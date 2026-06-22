package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// TestParseWorkflowFileMdPerStep asserts a workflow yaml whose step references an
// external md file (file: foo.md) expands that file's frontmatter into the step
// params and its body into the prompt (T4.2).
func TestParseWorkflowFileMdPerStep(t *testing.T) {
	dir := t.TempDir()

	md := `---
project_key: my-proj
agent: codex
runner: local
tags: [gen, ci]
timeout_sec: 90
---
Implement the feature described in the ticket.
Keep the change minimal.`
	if err := os.WriteFile(filepath.Join(dir, "gen.md"), []byte(md), 0o600); err != nil {
		t.Fatalf("write md: %v", err)
	}

	wf := `title: md-per-step
steps:
  - name: gen
    file: gen.md
  - name: test
    project_key: my-proj
    agent: exec
    runner: local
    cmd: [bash, -c, "go test ./..."]
`
	path := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(path, []byte(wf), 0o600); err != nil {
		t.Fatalf("write wf: %v", err)
	}

	spec, err := parseWorkflowFile(path)
	if err != nil {
		t.Fatalf("parseWorkflowFile: %v", err)
	}
	if len(spec.Steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(spec.Steps))
	}
	s1 := spec.Steps[0]
	// Inline name wins; the rest comes from the md frontmatter.
	if s1.Name != "gen" || s1.ProjectKey != "my-proj" || s1.Agent != "codex" || s1.Runner != "local" {
		t.Fatalf("md step1 fields wrong: %+v", s1)
	}
	if s1.TimeoutSec != 90 {
		t.Fatalf("md step1 timeout = %d, want 90", s1.TimeoutSec)
	}
	if len(s1.Tags) != 2 || s1.Tags[0] != "gen" || s1.Tags[1] != "ci" {
		t.Fatalf("md step1 tags = %v", s1.Tags)
	}
	// The md body became the prompt (trimmed).
	wantPrompt := "Implement the feature described in the ticket.\nKeep the change minimal."
	if s1.Prompt != wantPrompt {
		t.Fatalf("md step1 prompt = %q, want %q", s1.Prompt, wantPrompt)
	}
	// The CLI-only File field never crosses the wire (json:"-").
	if s1.File != "" {
		t.Fatalf("step1 File should be cleared after expansion, got %q", s1.File)
	}
	raw, _ := json.Marshal(s1)
	if string(raw) == "" || containsKey(raw, "file") {
		t.Fatalf("File must not serialize to JSON: %s", raw)
	}
	// step2 is a plain inline step, untouched.
	if spec.Steps[1].Agent != "exec" || len(spec.Steps[1].Cmd) != 3 {
		t.Fatalf("step2 inline step corrupted: %+v", spec.Steps[1])
	}
}

// TestParseWorkflowFileMdInlineOverride asserts an inline field on the step wins over
// the md frontmatter (the inline yaml is the override layer, T4.2).
func TestParseWorkflowFileMdInlineOverride(t *testing.T) {
	dir := t.TempDir()
	md := `---
project_key: from-md
agent: codex
runner: local
---
md body prompt`
	if err := os.WriteFile(filepath.Join(dir, "s.md"), []byte(md), 0o600); err != nil {
		t.Fatalf("write md: %v", err)
	}
	wf := `steps:
  - file: s.md
    project_key: inline-wins
    prompt: inline prompt wins
`
	path := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(path, []byte(wf), 0o600); err != nil {
		t.Fatalf("write wf: %v", err)
	}
	spec, err := parseWorkflowFile(path)
	if err != nil {
		t.Fatalf("parseWorkflowFile: %v", err)
	}
	s := spec.Steps[0]
	if s.ProjectKey != "inline-wins" {
		t.Fatalf("inline project_key should win, got %q", s.ProjectKey)
	}
	if s.Agent != "codex" {
		t.Fatalf("md agent should fill the unset field, got %q", s.Agent)
	}
	if s.Prompt != "inline prompt wins" {
		t.Fatalf("inline prompt should win over md body, got %q", s.Prompt)
	}
}

// TestParseWorkflowFileJSON asserts a .json workflow file (the export round-trip
// shape) decodes via the JSON branch (T4.1 import).
func TestParseWorkflowFileJSON(t *testing.T) {
	dir := t.TempDir()
	spec := job.WorkflowSpec{
		Title: "from-json",
		Steps: []job.StepSpec{
			{Name: "a", ProjectKey: "p", Agent: "exec", Runner: "local", Cmd: []string{"echo", "hi"}},
		},
	}
	raw, _ := json.MarshalIndent(spec, "", "  ")
	path := filepath.Join(dir, "wf.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write json: %v", err)
	}
	got, err := parseWorkflowFile(path)
	if err != nil {
		t.Fatalf("parseWorkflowFile(json): %v", err)
	}
	if got.Title != "from-json" || len(got.Steps) != 1 || got.Steps[0].Name != "a" {
		t.Fatalf("json decode wrong: %+v", got)
	}
}

// TestParseWorkflowFileJSONByContent asserts JSON is imported by CONTENT (leading
// '{'), not just the .json extension — so an exported `-f json` spec re-imports even
// when piped to a non-.json file name.
func TestParseWorkflowFileJSONByContent(t *testing.T) {
	dir := t.TempDir()
	raw, _ := json.MarshalIndent(job.WorkflowSpec{
		Title: "content-json",
		Steps: []job.StepSpec{{Name: "a", ProjectKey: "p", Agent: "exec", Runner: "local", Cmd: []string{"echo", "hi"}}},
	}, "", "  ")
	path := filepath.Join(dir, "wf.txt") // NOT .json — must be detected by content
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := parseWorkflowFile(path)
	if err != nil {
		t.Fatalf("parseWorkflowFile(.txt with json content): %v", err)
	}
	if got.Title != "content-json" || len(got.Steps) != 1 {
		t.Fatalf("content-json decode wrong: %+v", got)
	}
}

// TestExpandStepMarkdownMissingFile surfaces a clear error when the referenced md
// file does not exist.
func TestExpandStepMarkdownMissingFile(t *testing.T) {
	step := job.StepSpec{File: "nope.md"}
	if err := expandStepMarkdown(&step, t.TempDir(), 1); err == nil {
		t.Fatal("expected an error for a missing md file")
	}
}

// TestExpandStepMarkdownNoFrontmatter rejects an md file with no '---' frontmatter
// (the md-per-step contract requires explicit step params).
func TestExpandStepMarkdownNoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("just a body, no frontmatter"), 0o600); err != nil {
		t.Fatalf("write md: %v", err)
	}
	step := job.StepSpec{File: "x.md"}
	if err := expandStepMarkdown(&step, dir, 1); err == nil {
		t.Fatal("expected an error for an md file with no frontmatter")
	}
}

// containsKey is a tiny helper: does the JSON object contain the given top-level key?
func containsKey(raw []byte, key string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
