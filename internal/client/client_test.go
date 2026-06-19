package client

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

const testToken = "dev-token"

// openTestStore opens a metadata store under root (cleaned up automatically) for
// wiring a job.Service in tests.
func openTestStore(t *testing.T, root string) *jobstore.Store {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// newServer wires a real in-memory httpapi server with a single "self" project
// that allows the exec agent + local runner + raw exec, and returns an httptest
// server fronting it plus its temp storage root.
func newServer(t *testing.T, token string, allowEmpty bool) *httptest.Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: token, AllowEmptyToken: allowEmpty},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, root))
	srv := httpapi.New(&cfg.Server, token, allowEmpty, jobs, projects, agents, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// waitDone polls the client until the job reaches a terminal state.
func waitDone(t *testing.T, c *Client, id string) job.JobResult {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res, err := c.GetJob(id)
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		switch res.Status {
		case job.StatusDone, job.StatusFailed, job.StatusCancelled, job.StatusTimeout:
			return res
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach terminal state", id)
	return job.JobResult{}
}

func TestSubmitGetLogs(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	created, err := c.SubmitJob(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("no job id: %+v", created)
	}

	final := waitDone(t, c, created.ID)
	if final.Status != job.StatusDone {
		t.Fatalf("status=%s want done (err=%s)", final.Status, final.Error)
	}
	if final.ExitCode != 0 {
		t.Fatalf("exit_code=%d want 0", final.ExitCode)
	}

	out, err := c.GetLogs(created.ID, "stdout")
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if !strings.Contains(out, "go version") {
		t.Fatalf("stdout log missing output: %q", out)
	}
}

func TestCancelCompletedStable(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	created, err := c.SubmitJob(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}
	final := waitDone(t, c, created.ID)
	if final.Status != job.StatusDone {
		t.Fatalf("setup status=%s want done", final.Status)
	}

	after, err := c.CancelJob(created.ID)
	if err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	if after.Status != job.StatusDone {
		t.Fatalf("cancel of completed job changed status to %s", after.Status)
	}
}

func TestCancelUnknownJobError(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	_, err := c.CancelJob("nope")
	if err == nil {
		t.Fatal("expected error cancelling unknown job")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention 404 status: %v", err)
	}
}

func TestAuthMissingTokenRejected(t *testing.T) {
	ts := newServer(t, testToken, false) // server requires a token
	c := New(ts.URL, "")                 // client sends none

	_, err := c.SubmitJob(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".",
	})
	if err == nil {
		t.Fatal("expected 401 error without token")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error should mention 401: %v", err)
	}
}

func TestAuthCorrectTokenSucceeds(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	_, err := c.SubmitJob(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".",
	})
	if err != nil {
		t.Fatalf("SubmitJob with token: %v", err)
	}
}

func TestSubmitUnknownProjectFriendlyError(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	_, err := c.SubmitJob(job.JobRequest{
		ProjectKey: "ghost", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".",
	})
	if err == nil {
		t.Fatal("expected error for unknown project")
	}
	// Friendly error must carry the server's error summary.
	if !strings.Contains(err.Error(), "404") || !strings.Contains(strings.ToLower(err.Error()), "project") {
		t.Fatalf("error not friendly enough: %v", err)
	}
}

func TestGetLogsInvalidStream(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)
	if _, err := c.GetLogs("x", "nope"); err == nil {
		t.Fatal("expected error for invalid stream")
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8765":      "http://127.0.0.1:8765",
		"0.0.0.0:8765":        "http://127.0.0.1:8765",
		"http://0.0.0.0:9000": "http://127.0.0.1:9000",
		"http://host:8080/":   "http://host:8080",
		"https://example.com": "https://example.com",
		"0.0.0.0":             "http://127.0.0.1",
		"localhost:8765":      "http://localhost:8765",
	}
	for in, want := range cases {
		if got := NormalizeBaseURL(in); got != want {
			t.Errorf("NormalizeBaseURL(%q)=%q want %q", in, got, want)
		}
	}
}
