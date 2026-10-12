package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/delivery"
)

// GIT-02: a worktree job of a project with delivery.mode merge is merged into the
// main checkout once done, and the outcome is recorded on the job.
func TestAutoDeliveryMergesDoneWorktreeJob(t *testing.T) {
	repo, _ := gitRepo(t)
	sha := payloadCommit(t, repo, "payload", "delivered.txt", "from the job\n")
	s := newWorktreeService(t, repo, t.TempDir())
	s.cfg.Load().Projects["repo"] = func(p config.ProjectConfig) config.ProjectConfig {
		p.Delivery = &config.DeliveryConfig{Mode: "merge"}
		return p
	}(s.cfg.Load().Projects["repo"])
	j := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "-c", "user.email=t@e.x", "-c", "user.name=t", "cherry-pick", sha}, Cwd: ".", TimeoutSec: 60, Worktree: true,
	})
	if j.Status != StatusDone || j.CommitsAhead != 1 {
		t.Fatalf("job: status=%s ahead=%d", j.Status, j.CommitsAhead)
	}
	s.WaitTerminalHooks(10 * time.Second)
	got, _ := s.Get(j.ID)
	if got.Delivery == nil || got.Delivery.State != delivery.StateMerged || got.Delivery.Base != "main" {
		t.Fatalf("delivery = %+v", got.Delivery)
	}
	if _, err := os.Stat(filepath.Join(repo, "delivered.txt")); err != nil {
		t.Fatal("the job's commit did not reach main")
	}
	rec, _, _ := s.meta.GetJob(j.ID)
	if !strings.Contains(rec.DeliveryJSON, `"state":"merged"`) {
		t.Fatalf("not persisted: %q", rec.DeliveryJSON)
	}
	// A second manual delivery of a merged branch is refused by git cleanly (nothing to
	// merge is still a success for git) — and push is refused without allow_push.
	res, err := s.Deliver(j.ID, DeliverOptions{Mode: "push"})
	if err != nil || res.State != delivery.StateFailed || !strings.Contains(res.Error, "allow_push") {
		t.Fatalf("push without allow_push: %+v %v", res, err)
	}
}

// No delivery configured: nothing happens; a job without commits is skipped.
func TestAutoDeliveryOnlyWhenAsked(t *testing.T) {
	repo, _ := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	j := submitAndWait(t, s, JobRequest{ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "status"}, Cwd: ".", TimeoutSec: 60, Worktree: true, Deliver: "merge"})
	s.WaitTerminalHooks(10 * time.Second)
	got, _ := s.Get(j.ID)
	if got.Delivery != nil {
		t.Fatalf("a job with no commits was delivered: %+v", got.Delivery)
	}
	plain := submitAndWait(t, s, JobRequest{ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "status"}, Cwd: ".", TimeoutSec: 60, Worktree: true})
	s.WaitTerminalHooks(10 * time.Second)
	if g, _ := s.Get(plain.ID); g.Delivery != nil {
		t.Fatalf("delivered without a mode: %+v", g.Delivery)
	}
	if _, err := s.Deliver(plain.ID, DeliverOptions{}); err == nil || !strings.Contains(err.Error(), "mode is none") {
		t.Fatalf("manual delivery without a mode: %v", err)
	}
}

func TestRenderDeliveryBranchAndBody(t *testing.T) {
	snap := JobResult{ID: "20261012-120000-abcd1234", Title: "Fix the Login bug!", Commits: []Commit{{SHA: "0123456789ab", Subject: "fix login"}},
		Acceptance: "- login works", RequestJSON: `{"prompt":"fix login; token=sk-abcdefghijklmnopqrstuvwxyz0123"}`}
	if got := renderDeliveryBranch("agent/{{short_id}}-{{slug}}", snap); got != "agent/abcd1234-fix-the-login-bug" {
		t.Fatalf("branch = %s", got)
	}
	if got := renderDeliveryBranch("", snap); got != "gofer/20261012-120000-abcd1234" {
		t.Fatalf("default branch = %s", got)
	}
	body := (&Service{}).deliveryBody(&config.Config{Server: config.ServerConfig{WebBaseURL: "http://gofer.local/"}}, snap, requestOfJob(snap))
	for _, want := range []string{"## 任务", "## 验收标准", "`01234567` fix login", "http://gofer.local/jobs/20261012-120000-abcd1234"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "sk-abcdefghijklmnopqrstuvwxyz0123") {
		t.Fatalf("secret in the body:\n%s", body)
	}
}
