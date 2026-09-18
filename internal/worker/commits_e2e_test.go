package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
)

// TestOutcomeCarriesCommits proves the SUP-01 C capture travels the WHOLE worker
// path: the executing machine records the commit the job started from and the
// commits it produced, the Outcome frame carries both back, and the hub job row
// (in memory and in the store) ends up with them.
//
// The worker's checkout is a real temp git repository, so the base sha and the
// commit list are compared against the actual repository state rather than
// against values the test injected.
func TestOutcomeCarriesCommits(t *testing.T) {
	hub := buildHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	var base, sha1, sha2, hostRepo string
	cl, _ := buildWorkerSideJobsOpts(t, hub.ts.URL, workerSideOpts{
		PrepareHost: func(host string) {
			hostRepo = host
			base = gitInitRepo(t, host)
			sha1 = gitPayloadCommit(t, host, "one.txt", "payload one")
			sha2 = gitPayloadCommit(t, host, "two.txt", "payload two")
		},
	})
	clientErr := make(chan error, 1)
	go func() { clientErr <- cl.Run(ctx) }()
	waitWorkerOnline(t, hub.hub)

	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: []string{"git", "cherry-pick", sha1, sha2}, Cwd: ".", TimeoutSec: 60,
	})
	if created.ID == "" {
		t.Fatal("created job has no id")
	}
	final, ok := hub.jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("hub job %s not found", created.ID)
	}
	if final.Status != job.StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}

	got, ok := hub.jobs.Get(created.ID)
	if !ok {
		t.Fatalf("host Get(%s) not found", created.ID)
	}
	if got.BaseSHA != base {
		t.Fatalf("host base_sha = %q, want the worker checkout's base %q", got.BaseSHA, base)
	}
	if len(got.Commits) != 2 {
		t.Fatalf("host commits = %+v, want the 2 commits the worker produced", got.Commits)
	}
	if got.Commits[0].Subject != "payload two" || got.Commits[1].Subject != "payload one" {
		t.Fatalf("host commits must be newest first: %+v", got.Commits)
	}
	if got.Commits[0].SHA != gitRevParse(t, hostRepo, "HEAD") {
		t.Fatalf("host commits[0] = %+v, want the worker checkout's HEAD", got.Commits[0])
	}

	// The same values are what the persisted row carries (the host entry is evicted
	// at terminal, so a later `job show` reads the row).
	rec, ok, err := hub.store.GetJob(created.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob persisted: ok=%v err=%v", ok, err)
	}
	if rec.BaseSHA != base || !strings.Contains(rec.CommitsJSON, "payload two") {
		t.Fatalf("persisted commits mismatch: base_sha=%q commits_json=%q", rec.BaseSHA, rec.CommitsJSON)
	}

	cancel()
	select {
	case <-clientErr:
	case <-time.After(3 * time.Second):
		t.Log("worker client did not exit promptly after cancel (non-fatal)")
	}
}

// gitInitRepo turns dir into a git repository with one commit and returns that
// commit's sha (the base a job dispatched there started from).
func gitInitRepo(t *testing.T, dir string) string {
	t.Helper()
	gitRun(t, dir, "init", "-q", "-b", "main")
	gitRun(t, dir, "config", "user.email", "gofer-test@example.com")
	gitRun(t, dir, "config", "user.name", "gofer-test")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	gitRun(t, dir, "config", "core.autocrlf", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("worker fixture\n"), 0o600); err != nil {
		t.Fatalf("write README: %v", err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "init")
	return gitRevParse(t, dir, "HEAD")
}

// gitPayloadCommit commits one file on a throwaway branch and returns its sha,
// leaving the checkout back on main — so the dispatched job can apply it with
// `git cherry-pick` (one argv, no shell).
func gitPayloadCommit(t *testing.T, repo, file, subject string) string {
	t.Helper()
	gitRun(t, repo, "checkout", "-q", "-b", "payload-"+strings.TrimSuffix(file, ".txt"))
	if err := os.WriteFile(filepath.Join(repo, file), []byte(subject+"\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	gitRun(t, repo, "add", "--", file)
	gitRun(t, repo, "commit", "-q", "-m", subject)
	sha := gitRevParse(t, repo, "HEAD")
	gitRun(t, repo, "checkout", "-q", "main")
	return sha
}

func gitRevParse(t *testing.T, repo, rev string) string {
	t.Helper()
	return gitRun(t, repo, "rev-parse", rev)
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}
