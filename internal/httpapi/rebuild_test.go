package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/secret"
)

func requestFromStoredJob(t *testing.T, s *Server, id string) job.JobRequest {
	t.Helper()
	stored, ok := s.jobs.Get(id)
	if !ok {
		t.Fatalf("stored job %s not found", id)
	}
	var req job.JobRequest
	if err := json.Unmarshal([]byte(stored.RequestJSON), &req); err != nil {
		t.Fatalf("request_json not valid JSON: %v (%q)", err, stored.RequestJSON)
	}
	return req
}

func TestRebuildEndpointEmptyOverrides(t *testing.T) {
	s := newTestServer(t, testToken, false)
	srcID := createExec(t, s, []string{"go", "version"})
	waitDone(t, s, srcID)

	resp := do(t, s, http.MethodPost, "/v1/jobs/"+srcID+"/rebuild", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rebuild status=%d, want 200", resp.StatusCode)
	}
	var rebuilt job.JobResult
	decode(t, resp, &rebuilt)
	if rebuilt.ID == "" || rebuilt.ID == srcID {
		t.Fatalf("rebuilt id invalid: %+v", rebuilt)
	}
	if rebuilt.SourceJobID != srcID {
		t.Fatalf("source_job_id = %q, want %q", rebuilt.SourceJobID, srcID)
	}
	req := requestFromStoredJob(t, s, rebuilt.ID)
	if req.RequestID != "" || req.SessionID != "" {
		t.Fatalf("rebuild should clear request/session id, got %q/%q", req.RequestID, req.SessionID)
	}
}

func TestRebuildEndpointEnvSet(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		Env: map[string]string{"A": "1", "KEEP": "2"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var src job.JobResult
	decode(t, resp, &src)
	waitDone(t, s, src.ID)

	resp = do(t, s, http.MethodPost, "/v1/jobs/"+src.ID+"/rebuild", testToken, job.RebuildOverrides{
		EnvSet: map[string]string{"A": "9", "B": "3"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rebuild status=%d, want 200", resp.StatusCode)
	}
	var rebuilt job.JobResult
	decode(t, resp, &rebuilt)
	req := requestFromStoredJob(t, s, rebuilt.ID)
	if req.Env["A"] != "9" || req.Env["B"] != "3" || req.Env["KEEP"] != "2" {
		t.Fatalf("env_set merge wrong: %#v", req.Env)
	}
}

func TestRebuildEndpointRejectsPlaceholderAndUnknown(t *testing.T) {
	s := newTestServer(t, testToken, false)
	srcID := createExec(t, s, []string{"go", "version"})
	waitDone(t, s, srcID)

	prompt := "token=" + secret.Placeholder
	resp := do(t, s, http.MethodPost, "/v1/jobs/"+srcID+"/rebuild", testToken, job.RebuildOverrides{Prompt: &prompt})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("placeholder rebuild status=%d, want 400", resp.StatusCode)
	}

	resp = do(t, s, http.MethodPost, "/v1/jobs/does-not-exist/rebuild", testToken, job.RebuildOverrides{})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown rebuild status=%d, want 404", resp.StatusCode)
	}
}
