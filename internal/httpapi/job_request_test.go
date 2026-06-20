package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// TestGetJobRequestReturnsOriginal exercises GET /v1/jobs/{id}/request (P2-b):
// the endpoint echoes the original JobRequest fields (project/agent/cmd/tags)
// from the persisted request_json column, separate from get_job (D1).
func TestGetJobRequestReturnsOriginal(t *testing.T) {
	s := newTestServer(t, testToken, false)

	// Submit a job carrying a distinctive cmd + tags so we can assert round-trip.
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		Tags: []string{"alpha", "beta"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatalf("created job has no id: %+v", created)
	}
	waitDone(t, s, created.ID)

	rr := do(t, s, http.MethodGet, "/v1/jobs/"+created.ID+"/request", testToken, nil)
	if rr.StatusCode != http.StatusOK {
		t.Fatalf("get request status=%d, want 200", rr.StatusCode)
	}
	var got job.JobRequest
	decode(t, rr, &got)
	if got.ProjectKey != "self" || got.Agent != "exec" {
		t.Fatalf("request fields wrong: %+v", got)
	}
	if len(got.Cmd) != 2 || got.Cmd[0] != "go" || got.Cmd[1] != "version" {
		t.Fatalf("cmd not round-tripped: %+v", got.Cmd)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "alpha" || got.Tags[1] != "beta" {
		t.Fatalf("tags not round-tripped: %+v", got.Tags)
	}
}

// TestGetJobRequestUnknownID returns 404 for an id the server never saw, without
// panicking.
func TestGetJobRequestUnknownID(t *testing.T) {
	s := newTestServer(t, testToken, false)
	rr := do(t, s, http.MethodGet, "/v1/jobs/does-not-exist/request", testToken, nil)
	if rr.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rr.StatusCode)
	}
}
