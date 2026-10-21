package workflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestResolveStepRefs pins S2 / design 一·3: `ref:` is resolved to the full commit sha at
// submit, so every fan of the step checks out ONE tree, whatever spelling the template
// used. A ref together with worktree is refused (a throwaway checkout vs a deliverable).
func TestResolveStepRefs(t *testing.T) {
	repo, head := refTestRepo(t)
	payload := refTestCommit(t, repo, "candidate.txt", "candidate\n")
	refTestGit(t, repo, "tag", "v2.0.0", head)

	cfg := &config.Config{Projects: map[string]config.ProjectConfig{
		"proj": {HostPath: repo, AllowedAgents: []string{"exec"}, AllowedRunners: []string{"local"}, AllowExec: true},
	}}
	mk := func(mut func(*StepSpec)) Spec {
		st := StepSpec{Name: "checks", ProjectKey: "proj", Agent: "exec", Runner: "local", Cmd: []string{"true"}}
		mut(&st)
		return Spec{Steps: []StepSpec{st}}
	}

	cases := []struct {
		name    string
		spec    Spec
		want    string
		wantErr string
	}{
		{"branch", mk(func(s *StepSpec) { s.Ref = "main" }), head, ""},
		{"tag", mk(func(s *StepSpec) { s.Ref = "v2.0.0" }), head, ""},
		{"short sha", mk(func(s *StepSpec) { s.Ref = payload[:8] }), payload, ""},
		{"full sha", mk(func(s *StepSpec) { s.Ref = payload }), payload, ""},
		{"unknown ref", mk(func(s *StepSpec) { s.Ref = "no-such-ref" }), "", "no-such-ref"},
		{"ref and worktree", mk(func(s *StepSpec) { s.Ref = "main"; s.Worktree = true }), "", "mutually exclusive"},
		{"no ref", mk(func(s *StepSpec) {}), "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveStepRefs(tc.spec, cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("resolveStepRefs = %v, want an error naming %q", err, tc.wantErr)
				}
				assertInvalidRequest(t, err)
				return
			}
			if err != nil {
				t.Fatalf("resolveStepRefs = %v, want nil", err)
			}
			if got.Steps[0].Ref != tc.want {
				t.Fatalf("ref = %q, want %q", got.Steps[0].Ref, tc.want)
			}
		})
	}

	// The resolution reaches a nested sub-workflow's steps too: a child step runs on the
	// same pinned commit, not on whatever the branch points at when it starts.
	sub := mk(func(s *StepSpec) { s.Ref = "main" })
	parent := Spec{Steps: []StepSpec{{Name: "wf", ProjectKey: "proj", Agent: "exec", Runner: "local", Type: stepTypeWorkflow, SubWorkflow: &sub}}}
	got, err := resolveStepRefs(parent, cfg)
	if err != nil {
		t.Fatalf("resolveStepRefs(sub_workflow) = %v", err)
	}
	if ref := got.Steps[0].SubWorkflow.Steps[0].Ref; ref != head {
		t.Fatalf("sub-workflow ref = %q, want the resolved sha %q", ref, head)
	}
}

// refTestRepo creates the minimal checkout a ref resolution needs (no config, no server).
func refTestRepo(t *testing.T) (repo, head string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("ref resolution tests require git on PATH: %v", err)
	}
	repo = t.TempDir()
	refTestGit(t, repo, "init", "-q", "-b", "main")
	refTestGit(t, repo, "config", "user.email", "gofer-test@example.com")
	refTestGit(t, repo, "config", "user.name", "gofer-test")
	refTestGit(t, repo, "config", "commit.gpgsign", "false")
	refTestGit(t, repo, "config", "core.autocrlf", "false")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("ref fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refTestGit(t, repo, "add", ".")
	refTestGit(t, repo, "commit", "-q", "-m", "init")
	return repo, refTestGit(t, repo, "rev-parse", "HEAD")
}

// refTestCommit commits one file on a throwaway branch and returns its sha (main untouched).
func refTestCommit(t *testing.T, repo, file, content string) string {
	t.Helper()
	refTestGit(t, repo, "checkout", "-q", "-b", "cand")
	if err := os.WriteFile(filepath.Join(repo, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	refTestGit(t, repo, "add", "--", file)
	refTestGit(t, repo, "commit", "-q", "-m", "payload")
	sha := refTestGit(t, repo, "rev-parse", "HEAD")
	refTestGit(t, repo, "checkout", "-q", "main")
	return sha
}

func refTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestResolveStepRefsInNestedRepo: a project root that holds several repositories (the
// step's cwd names the nested one) resolves the ref in the repository the step runs in,
// not in the project root — the root may be another repository or none at all.
func TestResolveStepRefsInNestedRepo(t *testing.T) {
	root := t.TempDir()
	repo, head := refTestRepo(t)
	nested := filepath.Join(root, "tools", "app")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo, nested); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{
		"proj": {HostPath: root, AllowedAgents: []string{"exec"}, AllowedRunners: []string{"local"}, AllowExec: true},
	}}
	spec := Spec{Steps: []StepSpec{{Name: "checks", ProjectKey: "proj", Agent: "exec", Runner: "local", Cwd: "tools/app", Ref: "main", Cmd: []string{"true"}}}}
	got, err := resolveStepRefs(spec, cfg)
	if err != nil {
		t.Fatalf("resolveStepRefs(nested) = %v, want the ref resolved in tools/app", err)
	}
	if got.Steps[0].Ref != head {
		t.Fatalf("ref = %q, want %q", got.Steps[0].Ref, head)
	}
}
