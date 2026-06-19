package httpapi

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// waitDoneTok polls GET /v1/jobs/{id} with an explicit bearer token (the shared
// waitDone hardcodes testToken, which the caller-scoped servers do not accept).
func waitDoneTok(t *testing.T, s *Server, id, token string) job.JobResult {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp := do(t, s, http.MethodGet, "/v1/jobs/"+id, token, nil)
		var jr job.JobResult
		decode(t, resp, &jr)
		switch jr.Status {
		case job.StatusDone, job.StatusFailed, job.StatusCancelled, job.StatusTimeout:
			return jr
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach terminal state in time", id)
	return job.JobResult{}
}

// newTestServerCfg builds a Server from an explicit ServerConfig (so tests can
// exercise the multi-caller Callers set), wiring the same single "self" project
// as newTestServer. The effective legacy token is sc.Token.
func newTestServerCfg(t *testing.T, sc config.ServerConfig) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  sc,
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
	st, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	jobs := job.NewService(cfg, projects, agents, runners, st)
	return New(&cfg.Server, sc.Token, sc.AllowEmptyToken, jobs, projects, agents, nil, nil, nil, nil)
}

// createJob posts a job with the given bearer token and returns the decoded
// JobResult plus the HTTP status.
func createJob(t *testing.T, s *Server, token string) (job.JobResult, int) {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", token, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	var created job.JobResult
	if resp.StatusCode == http.StatusOK {
		decode(t, resp, &created)
	} else {
		resp.Body.Close()
	}
	return created, resp.StatusCode
}

// TestUnknownTokenRejected: an unrelated token is 401 (multi-caller path).
func TestUnknownTokenRejected(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	resp := do(t, s, http.MethodGet, "/v1/projects", "tok-bogus", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 for unknown token", resp.StatusCode)
	}
}

// TestCallerStampedFromToken: a configured caller's token authenticates and the
// created job carries THAT caller id (server-stamped, not client-supplied).
func TestCallerStampedFromToken(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	created, status := createJob(t, s, "tok-alice")
	if status != http.StatusOK {
		t.Fatalf("create status=%d, want 200", status)
	}
	if created.CallerID != "alice" {
		t.Fatalf("job caller_id=%q, want alice", created.CallerID)
	}
	final := waitDoneTok(t, s, created.ID, "tok-alice")
	if final.CallerID != "alice" {
		t.Fatalf("persisted caller_id=%q, want alice", final.CallerID)
	}
}

// TestCallerIDNotSpoofable: a client-supplied caller_id in the body is
// overwritten by the authenticated identity.
func TestCallerIDNotSpoofable(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	resp := do(t, s, http.MethodPost, "/v1/jobs", "tok-alice", job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		CallerID: "attacker",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	if created.CallerID != "alice" {
		t.Fatalf("caller_id=%q, want alice (client value must be overridden)", created.CallerID)
	}
}

// TestMultiCallerEachAuthenticates: two callers each authenticate with their own
// token and their jobs carry the right id; each token rejects the other's job
// only insofar as identity is concerned (both are valid callers).
func TestMultiCallerEachAuthenticates(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
		},
	})

	a, st := createJob(t, s, "tok-alice")
	if st != http.StatusOK || a.CallerID != "alice" {
		t.Fatalf("alice: status=%d caller=%q", st, a.CallerID)
	}
	b, st := createJob(t, s, "tok-bob")
	if st != http.StatusOK || b.CallerID != "bob" {
		t.Fatalf("bob: status=%d caller=%q", st, b.CallerID)
	}

	// Filter by caller via the list endpoint.
	waitDoneTok(t, s, a.ID, "tok-alice")
	waitDoneTok(t, s, b.ID, "tok-bob")
	resp := do(t, s, http.MethodGet, "/v1/jobs?caller=alice", "tok-alice", nil)
	var body struct {
		Jobs []job.JobResult `json:"jobs"`
	}
	decode(t, resp, &body)
	if len(body.Jobs) != 1 || body.Jobs[0].CallerID != "alice" {
		t.Fatalf("caller filter returned %+v, want only alice's job", body.Jobs)
	}
}

// TestConstantTimePathRejectsEqualLengthWrongToken: a wrong token of the SAME
// length as a valid one is still rejected (constant-time compare path).
func TestConstantTimePathRejectsEqualLengthWrongToken(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "abcdefgh"}},
	})
	// Same length (8), different bytes.
	resp := do(t, s, http.MethodGet, "/v1/projects", "abcdefgX", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 for equal-length wrong token", resp.StatusCode)
	}
}

// TestLegacyTokenIsDefaultCaller: the legacy single Token still authenticates
// and stamps caller id "default" (back-compat).
func TestLegacyTokenIsDefaultCaller(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{Token: "legacy-tok"})
	created, status := createJob(t, s, "legacy-tok")
	if status != http.StatusOK {
		t.Fatalf("create status=%d, want 200", status)
	}
	if created.CallerID != "default" {
		t.Fatalf("legacy token caller_id=%q, want default", created.CallerID)
	}
}
