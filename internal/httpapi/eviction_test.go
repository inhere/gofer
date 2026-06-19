package httpapi

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// TestServeLogAfterEviction asserts that a finished job's stdout stays readable
// over HTTP after SP3 evicts its in-memory entry: serveLog resolves the result
// dir from the job's persisted ResultDir (Get's DB fallback), not the live map.
func TestServeLogAfterEviction(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	var created job.JobResult
	decode(t, resp, &created)
	final := waitDone(t, s, created.ID)
	if final.Status != job.StatusDone {
		t.Fatalf("setup: status=%s, want done", final.Status)
	}

	// GET /logs/stdout goes through serveLog -> Get (DB fallback) -> result_dir.
	logResp := do(t, s, http.MethodGet, "/v1/jobs/"+created.ID+"/logs/stdout", testToken, nil)
	if logResp.StatusCode != http.StatusOK {
		t.Fatalf("logs status=%d, want 200", logResp.StatusCode)
	}
	body, _ := io.ReadAll(logResp.Body)
	logResp.Body.Close()
	if !strings.Contains(string(body), "go version") {
		t.Fatalf("stdout log missing after eviction: %q", body)
	}
}

// TestListInteractionsAfterEviction raises and answers an interaction on a live
// job, drives it to terminal (cancel) so it is evicted, then asserts the
// interaction list endpoint still returns the answered interaction via the
// interactions.jsonl fallback (GetPersistedInteractions), not the now-empty
// in-memory state.
func TestListInteractionsAfterEviction(t *testing.T) {
	s := newTestServer(t, testToken, false)
	jobID := submitRunningJob(t, s)

	createResp := do(t, s, http.MethodPost, "/v1/jobs/"+jobID+"/interactions", testToken, createInteractionReq{
		Type: job.InteractionTypeQuestion, Prompt: "continue?",
	})
	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("create interaction status=%d, want 200", createResp.StatusCode)
	}
	var created job.Interaction
	decode(t, createResp, &created)

	answerResp := do(t, s, http.MethodPost,
		"/v1/jobs/"+jobID+"/interactions/"+created.ID+"/answer", testToken,
		answerInteractionReq{Answer: "yes"})
	if answerResp.StatusCode != http.StatusOK {
		t.Fatalf("answer status=%d, want 200", answerResp.StatusCode)
	}

	// Drive the job terminal so it is evicted from memory.
	do(t, s, http.MethodPost, "/v1/jobs/"+jobID+"/cancel", testToken, nil)
	final := waitDone(t, s, jobID)
	if !job.IsTerminal(final.Status) {
		t.Fatalf("setup: expected terminal, got %s", final.Status)
	}

	// List interactions: must still surface the answered one (jsonl fallback).
	listResp := do(t, s, http.MethodGet, "/v1/jobs/"+jobID+"/interactions", testToken, nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list interactions status=%d, want 200", listResp.StatusCode)
	}
	var body struct {
		Interactions []job.Interaction `json:"interactions"`
	}
	decode(t, listResp, &body)
	if len(body.Interactions) != 1 {
		t.Fatalf("expected 1 interaction after eviction, got %d", len(body.Interactions))
	}
	if body.Interactions[0].Status != job.InteractionAnswered || body.Interactions[0].Answer != "yes" {
		t.Fatalf("unexpected interaction after eviction: %+v", body.Interactions[0])
	}
}
