package job

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

func uncommittedGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func uncommittedWrite(t *testing.T, root, name, value string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUncommittedDetectsNewDirtyOnly(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	uncommittedGit(t, repo, "init")
	uncommittedWrite(t, repo, ".gitignore", "ignored.txt\n")
	uncommittedWrite(t, repo, "A.txt", "base")
	uncommittedWrite(t, repo, "D.txt", "base")
	uncommittedGit(t, repo, "add", ".gitignore", "A.txt", "D.txt")
	uncommittedGit(t, repo, "commit", "-m", "base")
	uncommittedWrite(t, repo, "A.txt", "already dirty")
	uncommittedWrite(t, repo, "B.txt", "already untracked")
	before := captureUncommitted(repo)
	uncommittedWrite(t, repo, "A.txt", "changed again")
	uncommittedWrite(t, repo, "C.txt", "new dirty")
	uncommittedWrite(t, repo, "D.txt", "committed change")
	uncommittedWrite(t, repo, "ignored.txt", "ignored")
	uncommittedGit(t, repo, "add", "D.txt")
	uncommittedGit(t, repo, "commit", "-m", "commit D")
	after := captureUncommitted(repo)
	if got, want := diffUncommitted(before, after, nil), []string{"A.txt", "C.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("new dirty = %v, want %v", got, want)
	}
	if got := diffUncommitted(before, after, []string{"C*"}); !reflect.DeepEqual(got, []string{"A.txt"}) {
		t.Fatalf("ignore C* = %v, want [A.txt]", got)
	}
	if !globMatch("**/tracked.txt", "tools/gofer/tracked.txt") || !globMatch("**/tracked.txt", "tracked.txt") {
		t.Fatal("double-star glob must match nested and root paths")
	}
	if got := uncommittedEnabled("exec", "warn"); got {
		t.Fatal("exec job must not detect uncommitted changes")
	}
}

func TestUncommittedNestedRepo(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal(err)
	}
	outer := t.TempDir()
	uncommittedGit(t, outer, "init")
	nested := filepath.Join(outer, "tools", "gofer")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	uncommittedGit(t, nested, "init")
	uncommittedWrite(t, nested, "tracked.txt", "base")
	uncommittedGit(t, nested, "add", "tracked.txt")
	uncommittedGit(t, nested, "commit", "-m", "base")
	before := captureUncommitted(outer)
	uncommittedWrite(t, nested, "tracked.txt", "dirty")
	if got := diffUncommitted(before, captureUncommitted(outer), nil); !reflect.DeepEqual(got, []string{"tools/gofer/tracked.txt"}) {
		t.Fatalf("nested dirty = %v", got)
	}
}

func TestUncommittedPolicyReviewAndResume(t *testing.T) {
	t.Parallel()
	files := []string{"a.go", "b.go"}
	if got := uncommittedDecision("review", files, "sid", 0, 1, false); got != "review" {
		t.Fatalf("review decision = %q", got)
	}
	if got := uncommittedDecision("resume", files, "sid", 0, 1, false); got != "resume" {
		t.Fatalf("resume decision = %q", got)
	}
	if got := uncommittedDecision("resume", files, "sid", 1, 1, true); got != "review" {
		t.Fatalf("still dirty after resume = %q", got)
	}
	if got := uncommittedDecision("resume", files, "", 0, 1, false); got != "review" {
		t.Fatalf("no session decision = %q", got)
	}
	if got := uncommittedDecision("warn", files, "sid", 0, 1, false); got != "warn" {
		t.Fatalf("warn decision = %q", got)
	}
	if prompt := uncommittedResumePrompt(files); !strings.Contains(prompt, "a.go") || !strings.Contains(prompt, "b.go") || !strings.Contains(prompt, "不要 push") {
		t.Fatalf("resume prompt = %q", prompt)
	}
}

type uncommittedRunner struct {
	repo  string
	calls atomic.Int32
}

func (r *uncommittedRunner) Name() string { return localrunner.Name }

func (r *uncommittedRunner) Run(_ context.Context, _ runner.Request) runner.Result {
	if r.calls.Add(1) == 1 {
		if err := os.WriteFile(filepath.Join(r.repo, "dirty.txt"), []byte("uncommitted"), 0o644); err != nil {
			return runner.Result{ExitCode: 1, Err: err}
		}
	}
	return runner.Result{ExitCode: 0, SessionID: "session-123456789"}
}

func uncommittedService(t *testing.T, repo, policy string, run runner.Runner) *Service {
	t.Helper()
	root := t.TempDir()
	max := 1
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Server:  config.ServerConfig{AutoResumeMax: &max},
		Projects: map[string]config.ProjectConfig{"self": {
			HostPath: repo, AllowedAgents: []string{"fake"}, AllowedRunners: []string{"local"}, OnUncommitted: policy,
		}},
		Agents: map[string]config.AgentConfig{"fake": {
			Type: agent.TypeCLIAgent, Command: "go", SessionResume: []string{"--resume", "{{session_id}}", "{{prompt}}"},
		}},
	}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	return drainOnClose(t, NewService(cfg, project.NewRegistry(cfg, ""), agent.NewRegistry(cfg),
		map[string]runner.Runner{localrunner.Name: run}, meta, nil))
}

func TestUncommittedPolicyEndToEnd(t *testing.T) {
	t.Parallel()
	for _, policy := range []string{"review", "resume"} {
		t.Run(policy, func(t *testing.T) {
			repo := t.TempDir()
			uncommittedGit(t, repo, "init")
			uncommittedWrite(t, repo, "base.txt", "base")
			uncommittedGit(t, repo, "add", "base.txt")
			uncommittedGit(t, repo, "commit", "-m", "base")
			run := &uncommittedRunner{repo: repo}
			s := uncommittedService(t, repo, policy, run)
			first := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "fake", Runner: "local", Prompt: "work", Cwd: ".", TimeoutSec: 30})
			if first.UncommittedCount != 1 || !reflect.DeepEqual(first.UncommittedFiles, []string{"dirty.txt"}) {
				t.Fatalf("first uncommitted = %+v", first)
			}
			if policy == "review" {
				if first.Status != StatusNeedsReview {
					t.Fatalf("review status = %s", first.Status)
				}
				return
			}
			if first.Status != StatusDone {
				t.Fatalf("resume source status = %s", first.Status)
			}
			deadline := time.Now().Add(5 * time.Second)
			var source JobResult
			for time.Now().Before(deadline) {
				source, _ = s.Get(first.ID)
				if source.AutoResumedBy != "" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if source.AutoResumedBy == "" {
				t.Fatal("resume did not submit a continuation")
			}
			continued, ok := s.Wait(source.AutoResumedBy)
			if !ok || continued.Status != StatusNeedsReview || continued.UncommittedCount != 1 {
				t.Fatalf("still dirty continuation = %+v, ok=%v", continued, ok)
			}
			if run.calls.Load() != 2 {
				t.Fatalf("runner calls = %d, want 2", run.calls.Load())
			}
		})
	}
}

func TestUncommittedOutcomePersists(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	finished := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	entry := &jobEntry{result: finished}
	s.applyOutcome(entry, &runner.Outcome{UncommittedFiles: []string{"a.go", "b.go"}, UncommittedCount: 2})
	if err := s.persist(entry.result); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get(finished.ID)
	if !ok || got.UncommittedCount != 2 || !reflect.DeepEqual(got.UncommittedFiles, []string{"a.go", "b.go"}) {
		t.Fatalf("worker outcome in DB = %+v, ok=%v", got, ok)
	}
}

type uncommittedRemoteRunner struct{}

func (*uncommittedRemoteRunner) Name() string { return localrunner.Name }
func (*uncommittedRemoteRunner) Run(context.Context, runner.Request) runner.Result {
	return runner.Result{ExitCode: 0, Outcome: &runner.Outcome{
		UncommittedFiles: []string{"remote.go"}, UncommittedCount: 1,
	}}
}

func TestUncommittedRemoteOutcomeReview(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	uncommittedGit(t, repo, "init")
	uncommittedWrite(t, repo, "base.txt", "base")
	uncommittedGit(t, repo, "add", "base.txt")
	uncommittedGit(t, repo, "commit", "-m", "base")
	s := uncommittedService(t, repo, "review", &uncommittedRemoteRunner{})
	got := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "fake", Runner: "local", Prompt: "work", Cwd: ".", TimeoutSec: 30})
	if got.Status != StatusNeedsReview || got.UncommittedCount != 1 || !reflect.DeepEqual(got.UncommittedFiles, []string{"remote.go"}) {
		t.Fatalf("host remote outcome = %+v", got)
	}
}

func TestUncommittedWorkerReportsWithoutDeciding(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	uncommittedGit(t, repo, "init")
	uncommittedWrite(t, repo, "base.txt", "base")
	uncommittedGit(t, repo, "add", "base.txt")
	uncommittedGit(t, repo, "commit", "-m", "base")
	run := &uncommittedRunner{repo: repo}
	s := uncommittedService(t, repo, "resume", run)
	s.SetUncommittedDecisionOnly(true)
	got := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "fake", Runner: "local", Prompt: "work", Cwd: ".", TimeoutSec: 30})
	if got.Status != StatusDone || got.UncommittedCount != 1 || got.AutoResumedBy != "" || run.calls.Load() != 1 {
		t.Fatalf("worker should only report: %+v, calls=%d", got, run.calls.Load())
	}
}
