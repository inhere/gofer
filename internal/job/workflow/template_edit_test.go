package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validTemplateYAML is a minimal authored template: a required project variable, a
// defaulted runner and one exec step.
const validTemplateYAML = `
title: "check ${vars.project}"
project: ${vars.project}
vars:
  project:
    required: true
    desc: project key
  runner:
    default: local
steps:
  - name: check
    project_key: ${vars.project}
    agent: exec
    runner: ${vars.runner}
    cmd: ["go", "version"]
    cwd: .
    timeout_sec: 60
`

// TestReadWorkflowTemplateSource covers the three sources in lookup order (plan S5).
func TestReadWorkflowTemplateSource(t *testing.T) {
	projectDir, globalDir := t.TempDir(), t.TempDir()
	projectFile := filepath.Join(projectDir, ".gofer", "workflows", "verify.yaml")
	if err := os.MkdirAll(filepath.Dir(projectFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectFile, []byte(validTemplateYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	globalFile := filepath.Join(globalDir, "shared.yaml")
	if err := os.WriteFile(globalFile, []byte(validTemplateYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	src, err := ReadWorkflowTemplateSource("verify", projectDir, globalDir)
	if err != nil {
		t.Fatalf("project source: %v", err)
	}
	if src.Source != "project" || src.Path != projectFile || src.Content != validTemplateYAML {
		t.Fatalf("project source = %+v, want the project file verbatim", src)
	}

	src, err = ReadWorkflowTemplateSource("shared", projectDir, globalDir)
	if err != nil {
		t.Fatalf("global source: %v", err)
	}
	if src.Source != "global" || src.Path != globalFile {
		t.Fatalf("global source = %+v, want the global file", src)
	}

	// A project file shadows a builtin of the same name.
	shadow := filepath.Join(projectDir, ".gofer", "workflows", "compare.yaml")
	if err := os.WriteFile(shadow, []byte(validTemplateYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if src, err = ReadWorkflowTemplateSource("compare", projectDir, globalDir); err != nil || src.Source != "project" {
		t.Fatalf("shadowed builtin source = %+v, %v; want project", src, err)
	}

	// A builtin has no path and serialises its spec as YAML.
	src, err = ReadWorkflowTemplateSource("compare", t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("builtin source: %v", err)
	}
	if src.Source != "builtin" || src.Path != "" {
		t.Fatalf("builtin source = %+v, want builtin with no path", src)
	}
	if !strings.Contains(src.Content, "steps:") || !strings.Contains(src.Content, "vars:") {
		t.Fatalf("builtin content is not a template YAML:\n%s", src.Content)
	}

	if _, err := ReadWorkflowTemplateSource("nope", projectDir, globalDir); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("unknown template err = %v, want ErrTemplateNotFound", err)
	}
	if _, err := ReadWorkflowTemplateSource("../x", projectDir, globalDir); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("traversal name err = %v, want ErrInvalidTemplate", err)
	}
}

// TestValidateWorkflowTemplateContent asserts the save-time checks: a valid body passes,
// and a body that cannot parse, has no steps, keeps an undefined variable or breaks a step
// field is rejected as ErrInvalidTemplate.
func TestValidateWorkflowTemplateContent(t *testing.T) {
	if _, err := ValidateWorkflowTemplateContent(validTemplateYAML); err != nil {
		t.Fatalf("valid template rejected: %v", err)
	}
	cases := []struct{ name, body, want string }{
		{"yaml", "steps: [", "cannot parse YAML"},
		{"no steps", "title: x\n", "no steps"},
		{"undefined var", "steps:\n  - name: s\n    agent: exec\n    prompt: ${vars.missing}\n", "undefined variable"},
		{"fan name", `
steps:
  - name: f
    agent: exec
    fan:
      - name: f1
        agent: exec
`, "reserved"},
		{"retry", `
steps:
  - name: r
    agent: exec
    on_failure: retry
`, "requires a retry block"},
	}
	for _, tc := range cases {
		_, err := ValidateWorkflowTemplateContent(tc.body)
		if err == nil {
			t.Fatalf("%s: accepted an invalid template", tc.name)
		}
		if !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("%s: err = %v, want ErrInvalidTemplate", tc.name, err)
		}
		if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

// TestSaveWorkflowTemplateWritesAndRejects asserts a rejected template never reaches the
// disk, a valid one is written as <name>.yaml, and a companion extension is replaced.
func TestSaveWorkflowTemplateWritesAndRejects(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "workflows")
	bad := "steps: ["
	if _, err := SaveWorkflowTemplate(dir, "verify", bad); err == nil {
		t.Fatal("saved an invalid template")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a rejected save created %s", dir)
	}

	// A stale .yml of the same name is removed so the name resolves to what was just saved.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "verify.yml"), []byte(validTemplateYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := SaveWorkflowTemplate(dir, "verify", validTemplateYAML)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if path != filepath.Join(dir, "verify.yaml") {
		t.Fatalf("path = %q, want <dir>/verify.yaml", path)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != validTemplateYAML {
		t.Fatalf("saved body = %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "verify.yml")); !os.IsNotExist(err) {
		t.Fatal("the stale verify.yml survived the save")
	}
	// The saved template is loadable by the run path.
	if _, err := ResolveWorkflowTemplate("verify", map[string]string{"project": "self"}, "", dir); err != nil {
		t.Fatalf("saved template does not resolve: %v", err)
	}

	if _, err := SaveWorkflowTemplate(dir, "bad/name", validTemplateYAML); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("invalid name err = %v, want ErrInvalidTemplate", err)
	}
}

// TestDeleteWorkflowTemplate asserts a delete removes the file and a missing name reports
// ErrTemplateNotFound.
func TestDeleteWorkflowTemplate(t *testing.T) {
	dir := t.TempDir()
	if _, err := DeleteWorkflowTemplate(dir, "verify"); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("delete missing err = %v, want ErrTemplateNotFound", err)
	}
	path, err := SaveWorkflowTemplate(dir, "verify", validTemplateYAML)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DeleteWorkflowTemplate(dir, "verify")
	if err != nil || got != path {
		t.Fatalf("delete = %q, %v; want %q", got, err, path)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the template file survived the delete")
	}
}

// TestCheckTemplateName pins the accepted name shape (it becomes a file name).
func TestCheckTemplateName(t *testing.T) {
	for _, ok := range []string{"verify", "release-1.2", "a_b.c"} {
		if err := CheckTemplateName(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "../x", "a/b", `a\b`, "a b"} {
		if err := CheckTemplateName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
