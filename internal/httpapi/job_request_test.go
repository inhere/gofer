package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/secret"
)

// TestGetJobRequestReturnsRedacted exercises GET /v1/jobs/{id}/request (P5):
// the endpoint returns a usable JobRequest shape but strips env values / secret-
// looking strings and clears re-submit/session/caller noise.
func TestGetJobRequestReturnsRedacted(t *testing.T) {
	s := newTestServer(t, testToken, false)

	// Submit a job carrying distinctive fields plus env so we can assert the new
	// redacted contract instead of the old verbatim request_json echo.
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		Tags: []string{"alpha", "beta"}, Env: map[string]string{"API_TOKEN": "sk-test-env"},
		RequestID: "request-1", SessionID: "session-1", CallerID: "spoofed",
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
	if rr.Header.Get("X-Gofer-Redacted") != "1" {
		t.Fatalf("missing X-Gofer-Redacted header")
	}
	// Assert on the RAW body first: the core invariant is that the env plaintext never
	// appears ANYWHERE in this response, not merely that Env["API_TOKEN"] holds the
	// placeholder. A field-only check would miss a leak through some other field.
	raw, err := io.ReadAll(rr.Body)
	_ = rr.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if bytes.Contains(raw, []byte("sk-test-env")) {
		t.Fatalf("env plaintext leaked somewhere in the response body: %s", raw)
	}
	var got job.JobRequest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got.ProjectKey != "self" || got.Agent != "exec" {
		t.Fatalf("request fields wrong: %+v", got)
	}
	if len(got.Cmd) != 2 || got.Cmd[0] != "go" || got.Cmd[1] != "version" {
		t.Fatalf("cmd not round-tripped: %+v", got.Cmd)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "alpha" || got.Tags[1] != "beta" {
		t.Fatalf("tags not round-tripped: %+v", got.Tags)
	}
	if got.Env["API_TOKEN"] != secret.Placeholder {
		t.Fatalf("env token = %q, want placeholder", got.Env["API_TOKEN"])
	}
	if got.Env["API_TOKEN"] == "sk-test-env" {
		t.Fatalf("env plaintext leaked: %+v", got.Env)
	}
	if got.RequestID != "" || got.SessionID != "" || got.CallerID != "" {
		t.Fatalf("request/session/caller should be cleared, got %q/%q/%q", got.RequestID, got.SessionID, got.CallerID)
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
