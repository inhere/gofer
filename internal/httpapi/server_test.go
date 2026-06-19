package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

const testToken = "dev-token"

// openTestStore opens a metadata store under root (cleaned up automatically) for
// wiring a job.Service in tests. Shared across the httpapi test files.
func openTestStore(t *testing.T, root string) *jobstore.Store {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// newTestServer builds a Server whose only project "self" allows the exec agent
// and has allow_exec=true. Results are isolated under a temp storage root so
// tests do not write into the repo tree. token is the effective bearer token;
// allowEmpty mirrors the allow_empty_token flag.
func newTestServer(t *testing.T, token string, allowEmpty bool) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: token, AllowEmptyToken: allowEmpty},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root, // any existing dir; cwd "." resolves here
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
	return New(&cfg.Server, token, allowEmpty, jobs, projects, agents)
}

// do performs an in-process request against the server's handler.
func do(t *testing.T, s *Server, method, path, token string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result()
}

func decode(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
}

func TestHealthNoAuth(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/health", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status=%d, want 200", resp.StatusCode)
	}
	var body map[string]any
	decode(t, resp, &body)
	if body["ok"] != true {
		t.Fatalf("health body missing ok=true: %v", body)
	}
}

func TestAuthRejectedWithoutHeader(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/projects", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", resp.StatusCode)
	}
}

func TestAuthRejectedWrongToken(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/projects", "wrong", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", resp.StatusCode)
	}
}

func TestAuthSuccess(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/projects", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Projects []string `json:"projects"`
	}
	decode(t, resp, &body)
	if len(body.Projects) != 1 || body.Projects[0] != "self" {
		t.Fatalf("unexpected projects: %v", body.Projects)
	}
}

func TestEmptyTokenAllowed(t *testing.T) {
	s := newTestServer(t, "", true)
	resp := do(t, s, http.MethodGet, "/v1/projects", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200 (empty token allowed)", resp.StatusCode)
	}
}

func TestEmptyTokenRejectedWhenNotAllowed(t *testing.T) {
	// New() with empty token + allowEmpty=false: every /v1 request is rejected.
	s := newTestServer(t, "", false)
	resp := do(t, s, http.MethodGet, "/v1/projects", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", resp.StatusCode)
	}
}

func TestGetProjectKnown(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/projects/self", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var pv projectView
	decode(t, resp, &pv)
	if pv.Key != "self" || !pv.AllowExec {
		t.Fatalf("unexpected project view: %+v", pv)
	}
}

func TestGetProjectUnknown(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/projects/nope", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
	var eb errorBody
	decode(t, resp, &eb)
	if eb.Error == "" {
		t.Fatalf("error body missing error field: %+v", eb)
	}
}

func TestListAgents(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/agents", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Agents []agentView `json:"agents"`
	}
	decode(t, resp, &body)
	// The built-in exec agent must be present and reported available (no CLI).
	var foundExec bool
	for _, a := range body.Agents {
		if a.Key == "exec" {
			foundExec = true
			if !a.Available {
				t.Fatalf("exec agent should be available, got %+v", a)
			}
		}
	}
	if !foundExec {
		t.Fatalf("exec agent missing from list: %+v", body.Agents)
	}
}

func TestCreateJobUnknownProject(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "ghost", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 (unknown project)", resp.StatusCode)
	}
}

func TestCreateJobUnknownAgent(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local",
		Prompt: "hi", Cwd: ".", TimeoutSec: 30,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (agent not allowed)", resp.StatusCode)
	}
}

func TestCreateJobExecAndPoll(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatalf("created job has no id: %+v", created)
	}

	final := waitDone(t, s, created.ID)
	if final.Status != job.StatusDone {
		t.Fatalf("job status=%s, want done (err=%s)", final.Status, final.Error)
	}

	// stdout log must contain the command output.
	logResp := do(t, s, http.MethodGet, "/v1/jobs/"+created.ID+"/logs/stdout", testToken, nil)
	if logResp.StatusCode != http.StatusOK {
		t.Fatalf("logs status=%d, want 200", logResp.StatusCode)
	}
	out, _ := io.ReadAll(logResp.Body)
	logResp.Body.Close()
	if !strings.Contains(string(out), "go version") {
		t.Fatalf("stdout log missing output: %q", out)
	}
}

func TestGetJobUnknown(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/jobs/does-not-exist", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}

func TestLogTailLimited(t *testing.T) {
	s := newTestServer(t, testToken, false)
	// Produce ~512KB of stdout, well over the 256KB tail cap. yes | head emits
	// many lines fast; we cap with head -c.
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "yes ABCDEFGH | head -c 524288"}, Cwd: ".", TimeoutSec: 30,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	final := waitDone(t, s, created.ID)
	if final.Status != job.StatusDone {
		t.Fatalf("job status=%s, want done (err=%s)", final.Status, final.Error)
	}

	logResp := do(t, s, http.MethodGet, "/v1/jobs/"+created.ID+"/logs/stdout", testToken, nil)
	out, _ := io.ReadAll(logResp.Body)
	logResp.Body.Close()
	if len(out) > maxLogTailBytes {
		t.Fatalf("log tail %d bytes exceeds cap %d", len(out), maxLogTailBytes)
	}
	if len(out) != maxLogTailBytes {
		t.Fatalf("expected exactly %d bytes (input was larger), got %d", maxLogTailBytes, len(out))
	}
}

func TestCancelCompletedJobStable(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	var created job.JobResult
	decode(t, resp, &created)
	final := waitDone(t, s, created.ID)
	if final.Status != job.StatusDone {
		t.Fatalf("setup: job status=%s, want done", final.Status)
	}

	// Cancelling a completed job is a stable no-op: 200 with the unchanged done
	// snapshot.
	cancelResp := do(t, s, http.MethodPost, "/v1/jobs/"+created.ID+"/cancel", testToken, nil)
	if cancelResp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status=%d, want 200", cancelResp.StatusCode)
	}
	var after job.JobResult
	decode(t, cancelResp, &after)
	if after.Status != job.StatusDone {
		t.Fatalf("status changed after no-op cancel: %s", after.Status)
	}
}

func TestCancelUnknownJob(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs/nope/cancel", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}

// waitDone polls the job HTTP API until the job reaches a terminal state.
func waitDone(t *testing.T, s *Server, id string) job.JobResult {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp := do(t, s, http.MethodGet, "/v1/jobs/"+id, testToken, nil)
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
