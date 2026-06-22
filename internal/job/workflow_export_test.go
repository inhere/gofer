package job

import (
	"strings"
	"testing"
)

// TestExportWorkflowRoundTrip asserts a submitted workflow exports back to a
// WorkflowSpec that faithfully reproduces the chain (title + every step's核心字段),
// so an export→import round-trips (T4.1).
func TestExportWorkflowRoundTrip(t *testing.T) {
	s := newTestService(t, t.TempDir())
	wf, err := s.SubmitWorkflow(WorkflowSpec{
		Title: "round-trip",
		Steps: []StepSpec{echoStep("gen"), echoStep("review"), echoStep("test")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	t.Cleanup(func() { _ = s.CancelWorkflow(wf.ID) })

	spec, ok, redacted, err := s.ExportWorkflow(wf.ID)
	if err != nil {
		t.Fatalf("ExportWorkflow: %v", err)
	}
	if !ok {
		t.Fatal("ExportWorkflow ok=false for an existing workflow")
	}
	if redacted {
		t.Fatal("a secret-free workflow should not report redacted=true")
	}
	if spec.Title != "round-trip" {
		t.Fatalf("title = %q, want round-trip", spec.Title)
	}
	if len(spec.Steps) != 3 {
		t.Fatalf("exported %d steps, want 3", len(spec.Steps))
	}
	if spec.Steps[0].Name != "gen" || spec.Steps[0].ProjectKey != "self" ||
		spec.Steps[0].Agent != "exec" || spec.Steps[0].Runner != "local" {
		t.Fatalf("step1 core fields not round-tripped: %+v", spec.Steps[0])
	}
	if got := strings.Join(spec.Steps[0].Cmd, " "); got != "sh -c echo gen" {
		t.Fatalf("step1 cmd = %q, want 'sh -c echo gen'", got)
	}
}

// TestExportWorkflowUnknownID asserts an unknown id reports ok=false (the HTTP layer
// maps it to a 404), not an error.
func TestExportWorkflowUnknownID(t *testing.T) {
	s := newTestService(t, t.TempDir())
	_, ok, _, err := s.ExportWorkflow("wf-does-not-exist")
	if err != nil {
		t.Fatalf("ExportWorkflow unknown id err = %v, want nil", err)
	}
	if ok {
		t.Fatal("ExportWorkflow ok=true for an unknown id")
	}
}

// TestExportWorkflowStripsSecrets asserts credential-looking values in a step's
// prompt / cmd / cwd are replaced with the placeholder on export (T4.1 / SR403), the
// non-secret structure survives, and redacted=true is reported.
func TestExportWorkflowStripsSecrets(t *testing.T) {
	s := newTestService(t, t.TempDir())
	wf, err := s.SubmitWorkflow(WorkflowSpec{
		Title: "with-secrets",
		Steps: []StepSpec{
			{
				Name: "leaky", ProjectKey: "self", Agent: "exec", Runner: "local",
				// secret in cmd (flag + env-style), non-secret args around it.
				Cmd: []string{
					"sh", "-c",
					"deploy --api-key=sk-live-ABCDEF123 --region us-east-1",
				},
				Cwd: ".", TimeoutSec: 30,
			},
		},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	t.Cleanup(func() { _ = s.CancelWorkflow(wf.ID) })

	spec, ok, redacted, err := s.ExportWorkflow(wf.ID)
	if err != nil || !ok {
		t.Fatalf("ExportWorkflow: ok=%v err=%v", ok, err)
	}
	if !redacted {
		t.Fatal("export carrying an --api-key should report redacted=true")
	}
	joined := strings.Join(spec.Steps[0].Cmd, " ")
	if strings.Contains(joined, "sk-live-ABCDEF123") {
		t.Fatalf("secret value leaked into export: %q", joined)
	}
	if !strings.Contains(joined, secretPlaceholder) {
		t.Fatalf("expected placeholder %q in export, got %q", secretPlaceholder, joined)
	}
	// Non-secret structure survives (the region flag + its value are untouched).
	if !strings.Contains(joined, "--region us-east-1") {
		t.Fatalf("non-secret args were corrupted: %q", joined)
	}
}

// TestRedactSecretsInString pins the redaction heuristic against the common secret
// shapes (flag / env-style / yaml-style) and proves a non-secret string is untouched.
func TestRedactSecretsInString(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantHit  bool
		mustGone string // substring that MUST NOT survive when wantHit
		mustKeep string // substring that MUST survive
	}{
		{"flag", "deploy --token sk-abc123 --verbose", true, "sk-abc123", "--verbose"},
		{"flag-eq", "run --api-key=KEY_999 next", true, "KEY_999", "next"},
		{"env-style", "export AWS_SECRET_ACCESS_KEY=zzz999 && go test", true, "zzz999", "go test"},
		{"yaml-style", "password: hunter2", true, "hunter2", "password"},
		{"no-secret", "go build ./... && echo done", false, "", "go build"},
		{"empty", "", false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, hit := redactSecretsInString(tc.in)
			if hit != tc.wantHit {
				t.Fatalf("redact(%q) hit=%v, want %v (out=%q)", tc.in, hit, tc.wantHit, out)
			}
			if tc.wantHit {
				if tc.mustGone != "" && strings.Contains(out, tc.mustGone) {
					t.Fatalf("secret %q survived redaction: %q", tc.mustGone, out)
				}
				if !strings.Contains(out, secretPlaceholder) {
					t.Fatalf("expected placeholder in %q", out)
				}
			} else if out != tc.in {
				t.Fatalf("non-secret string was modified: %q -> %q", tc.in, out)
			}
			if tc.mustKeep != "" && !strings.Contains(out, tc.mustKeep) {
				t.Fatalf("expected %q to survive in %q", tc.mustKeep, out)
			}
		})
	}
}

// TestRedactSecretsRecursesSubWorkflow asserts a secret nested inside an inline
// sub-workflow step is also stripped on export (T4.1 recursion).
func TestRedactSecretsRecursesSubWorkflow(t *testing.T) {
	spec := WorkflowSpec{
		Title: "parent",
		Steps: []StepSpec{
			{
				Name: "sub", ProjectKey: "self", Agent: "exec", Runner: "local",
				Type: stepTypeWorkflow,
				SubWorkflow: &WorkflowSpec{
					Steps: []StepSpec{
						{
							Name: "inner", ProjectKey: "self", Agent: "exec", Runner: "local",
							Prompt: "use bearer token=secrettok-XYZ to call the API",
							Cwd:    ".", TimeoutSec: 30,
						},
					},
				},
			},
		},
	}
	scrubbed, redacted, err := redactWorkflowSecrets(spec)
	if err != nil {
		t.Fatalf("redactWorkflowSecrets: %v", err)
	}
	if !redacted {
		t.Fatal("a secret in a sub-workflow step should report redacted=true")
	}
	inner := scrubbed.Steps[0].SubWorkflow.Steps[0].Prompt
	if strings.Contains(inner, "secrettok-XYZ") {
		t.Fatalf("sub-workflow secret leaked: %q", inner)
	}
	if !strings.Contains(inner, secretPlaceholder) {
		t.Fatalf("expected placeholder in sub-workflow prompt: %q", inner)
	}
	// The ORIGINAL spec must be untouched (export works on a copy).
	if !strings.Contains(spec.Steps[0].SubWorkflow.Steps[0].Prompt, "secrettok-XYZ") {
		t.Fatal("redactWorkflowSecrets mutated the input spec (must work on a copy)")
	}
}
