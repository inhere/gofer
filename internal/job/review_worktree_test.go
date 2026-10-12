package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/delivery"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// newPickService is newWorktreeService plus a resumable "picker" cli-agent whose
// prompt is a commit to cherry-pick: the first run picks one commit, a continuation
// (whose prompt is the review note) picks another — real commits on the job branch
// without a shell.
func newPickService(t *testing.T, repo, state string) *Service {
	t.Helper()
	pick := []string{"-c", "user.email=t@e.x", "-c", "user.name=t", "cherry-pick", "{{prompt}}"}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: state},
		Projects: map[string]config.ProjectConfig{
			"repo": {
				HostPath:       repo,
				AllowedAgents:  []string{"picker", "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"picker": {Type: agent.TypeCLIAgent, Command: "git", Args: pick, SessionResume: pick},
		},
	}
	meta, err := jobstore.Open(filepath.Join(state, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	return drainOnClose(t, NewService(cfg, project.NewRegistry(cfg, ""), agent.NewRegistry(cfg), runners, meta, nil))
}

// gofer-glyo: a rejected worktree job continued with reject --resume keeps working
// on the SAME worktree and branch, inherits the delivery settings, and is delivered
// once accepted. gofer-iy68: while it awaits review its delivery can be previewed,
// and the preview describes the original task, not the rework note.
func TestRejectResumeKeepsWorktreeAndDelivers(t *testing.T) {
	repo, _ := gitRepo(t)
	first := payloadCommit(t, repo, "p1", "one.txt", "one\n")
	second := payloadCommit(t, repo, "p2", "two.txt", "two\n")
	s := newPickService(t, repo, t.TempDir())

	src := submitAndWait(t, s, JobRequest{ProjectKey: "repo", Agent: "picker", Runner: "local",
		Prompt: first, Cwd: ".", TimeoutSec: 60, Worktree: true, Review: true, Deliver: "merge",
		SessionID: "sess-wt", Title: "Add the files",
		// The prompt is the commit to pick: keep the injected sections out of it.
		RulesResolved: true})
	if src.Status != StatusNeedsReview || src.WorktreePath == "" || src.CommitsAhead != 1 {
		t.Fatalf("setup: status=%s worktree=%q ahead=%d err=%s", src.Status, src.WorktreePath, src.CommitsAhead, src.Error)
	}

	out, err := s.RejectJob(src.ID, "bob", second, true)
	if err != nil || out.ResumeJobID == "" {
		t.Fatalf("reject --resume: %+v %v", out, err)
	}
	cont, _ := s.Wait(out.ResumeJobID)
	if cont.Status != StatusNeedsReview {
		t.Fatalf("continuation status = %s (err=%s)", cont.Status, cont.Error)
	}
	if cont.WorktreePath != src.WorktreePath || cont.WorktreeBranch != src.WorktreeBranch ||
		cont.WorktreeBaseSHA != src.WorktreeBaseSHA || cont.CommitsAhead != 2 {
		t.Fatalf("continuation worktree = %q %q base=%s ahead=%d, want the source's %q %q base=%s ahead=2",
			cont.WorktreePath, cont.WorktreeBranch, cont.WorktreeBaseSHA, cont.CommitsAhead,
			src.WorktreePath, src.WorktreeBranch, src.WorktreeBaseSHA)
	}
	if req := requestOfJob(cont); req.Deliver != "merge" {
		t.Fatalf("continuation deliver = %q, want the source's merge", req.Deliver)
	}

	p, err := s.PreviewDelivery(cont.ID, DeliverOptions{})
	if err != nil {
		t.Fatalf("preview at needs_review: %v", err)
	}
	if p.Mode != "merge" || p.Branch != src.WorktreeBranch || p.Title != "Add the files" ||
		!strings.Contains(p.Body, first) || strings.Contains(p.Body, second) {
		t.Fatalf("preview = %+v", p)
	}
	if _, err := s.Deliver(cont.ID, DeliverOptions{}); err == nil {
		t.Fatal("a job awaiting review must not be delivered")
	}

	if _, err := s.AcceptJob(cont.ID, "bob", ""); err != nil {
		t.Fatalf("accept: %v", err)
	}
	s.WaitTerminalHooks(10 * time.Second)
	got, _ := s.Get(cont.ID)
	if got.Delivery == nil || got.Delivery.State != delivery.StateMerged {
		t.Fatalf("delivery = %+v", got.Delivery)
	}
	for _, f := range []string{"one.txt", "two.txt"} {
		if _, err := os.Stat(filepath.Join(repo, f)); err != nil {
			t.Fatalf("%s did not reach main: %v", f, err)
		}
	}
}
