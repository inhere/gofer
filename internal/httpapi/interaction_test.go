package httpapi

import (
	"net/http"
	"testing"
	"time"

	"dev-agent-bridge/internal/job"
)

// submitRunningJob submits a long-lived exec job over the HTTP API and polls
// until it reports running, so interactions can be raised while the job is
// genuinely live. It registers a cleanup that cancels the job to stop its
// goroutine before the temp dirs are torn down.
func submitRunningJob(t *testing.T, s *Server) string {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "30"}, Cwd: ".", TimeoutSec: 60,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit running job: status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatalf("running job has no id: %+v", created)
	}
	waitRunning(t, s, created.ID)
	t.Cleanup(func() {
		do(t, s, http.MethodPost, "/v1/jobs/"+created.ID+"/cancel", testToken, nil)
	})
	return created.ID
}

// waitRunning polls GET /v1/jobs/{id} until the job is running.
func waitRunning(t *testing.T, s *Server, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp := do(t, s, http.MethodGet, "/v1/jobs/"+id, testToken, nil)
		var jr job.JobResult
		decode(t, resp, &jr)
		if jr.Status == job.StatusRunning || jr.Status == job.StatusPendingInteraction {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach running in time", id)
}

func TestInteractionLifecycle(t *testing.T) {
	s := newTestServer(t, testToken, false)
	jobID := submitRunningJob(t, s)

	// Create a question interaction.
	resp := do(t, s, http.MethodPost, "/v1/jobs/"+jobID+"/interactions", testToken, createInteractionReq{
		Type:   job.InteractionTypeQuestion,
		Prompt: "continue?",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created job.Interaction
	decode(t, resp, &created)
	if created.ID == "" || created.JobID != jobID {
		t.Fatalf("unexpected created interaction: %+v", created)
	}
	if created.Status != job.InteractionPending {
		t.Fatalf("created interaction status=%s, want pending", created.Status)
	}

	// List must contain the pending interaction.
	listResp := do(t, s, http.MethodGet, "/v1/jobs/"+jobID+"/interactions", testToken, nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d, want 200", listResp.StatusCode)
	}
	var list struct {
		Interactions []job.Interaction `json:"interactions"`
	}
	decode(t, listResp, &list)
	if len(list.Interactions) != 1 || list.Interactions[0].ID != created.ID {
		t.Fatalf("unexpected interactions list: %+v", list.Interactions)
	}
	if list.Interactions[0].Status != job.InteractionPending {
		t.Fatalf("listed interaction not pending: %+v", list.Interactions[0])
	}

	// Answer it.
	ansResp := do(t, s, http.MethodPost,
		"/v1/jobs/"+jobID+"/interactions/"+created.ID+"/answer", testToken,
		answerInteractionReq{Answer: "yes"})
	if ansResp.StatusCode != http.StatusOK {
		t.Fatalf("answer status=%d, want 200", ansResp.StatusCode)
	}
	var answered job.Interaction
	decode(t, ansResp, &answered)
	if answered.Status != job.InteractionAnswered || answered.Answer != "yes" {
		t.Fatalf("unexpected answered interaction: %+v", answered)
	}

	// List again confirms the answered state persisted.
	listResp2 := do(t, s, http.MethodGet, "/v1/jobs/"+jobID+"/interactions", testToken, nil)
	decode(t, listResp2, &list)
	if len(list.Interactions) != 1 || list.Interactions[0].Status != job.InteractionAnswered {
		t.Fatalf("expected answered after list, got %+v", list.Interactions)
	}
	if list.Interactions[0].Answer != "yes" {
		t.Fatalf("expected answer 'yes', got %+v", list.Interactions[0])
	}
}

func TestInteractionEmptyListIsArray(t *testing.T) {
	s := newTestServer(t, testToken, false)
	jobID := submitRunningJob(t, s)

	resp := do(t, s, http.MethodGet, "/v1/jobs/"+jobID+"/interactions", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d, want 200", resp.StatusCode)
	}
	var list struct {
		Interactions []job.Interaction `json:"interactions"`
	}
	decode(t, resp, &list)
	// Must be a non-nil empty array (serialises as []).
	if list.Interactions == nil || len(list.Interactions) != 0 {
		t.Fatalf("expected empty non-nil interactions array, got %v", list.Interactions)
	}
}

func TestInteractionUnknownJob(t *testing.T) {
	s := newTestServer(t, testToken, false)

	// Create on unknown job -> 404.
	resp := do(t, s, http.MethodPost, "/v1/jobs/nope/interactions", testToken, createInteractionReq{
		Type: job.InteractionTypeQuestion, Prompt: "q",
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("create unknown job status=%d, want 404", resp.StatusCode)
	}
	// List on unknown job -> 404.
	listResp := do(t, s, http.MethodGet, "/v1/jobs/nope/interactions", testToken, nil)
	if listResp.StatusCode != http.StatusNotFound {
		t.Fatalf("list unknown job status=%d, want 404", listResp.StatusCode)
	}
	// Answer on unknown job -> 404.
	ansResp := do(t, s, http.MethodPost, "/v1/jobs/nope/interactions/x/answer", testToken,
		answerInteractionReq{Answer: "a"})
	if ansResp.StatusCode != http.StatusNotFound {
		t.Fatalf("answer unknown job status=%d, want 404", ansResp.StatusCode)
	}
}

func TestInteractionCreateInvalidPayload(t *testing.T) {
	s := newTestServer(t, testToken, false)
	jobID := submitRunningJob(t, s)

	// Missing prompt -> 400.
	resp := do(t, s, http.MethodPost, "/v1/jobs/"+jobID+"/interactions", testToken, createInteractionReq{
		Type: job.InteractionTypeQuestion,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing prompt status=%d, want 400", resp.StatusCode)
	}
	// Bad type -> 400.
	resp2 := do(t, s, http.MethodPost, "/v1/jobs/"+jobID+"/interactions", testToken, createInteractionReq{
		Type: "bogus", Prompt: "q",
	})
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad type status=%d, want 400", resp2.StatusCode)
	}
}

func TestInteractionAnswerUnknownInteraction(t *testing.T) {
	s := newTestServer(t, testToken, false)
	jobID := submitRunningJob(t, s)

	resp := do(t, s, http.MethodPost,
		"/v1/jobs/"+jobID+"/interactions/ghost/answer", testToken,
		answerInteractionReq{Answer: "a"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("answer unknown interaction status=%d, want 404", resp.StatusCode)
	}
}

func TestInteractionDoubleAnswer(t *testing.T) {
	s := newTestServer(t, testToken, false)
	jobID := submitRunningJob(t, s)

	createResp := do(t, s, http.MethodPost, "/v1/jobs/"+jobID+"/interactions", testToken, createInteractionReq{
		Type: job.InteractionTypeQuestion, Prompt: "q",
	})
	var created job.Interaction
	decode(t, createResp, &created)

	first := do(t, s, http.MethodPost,
		"/v1/jobs/"+jobID+"/interactions/"+created.ID+"/answer", testToken,
		answerInteractionReq{Answer: "a"})
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first answer status=%d, want 200", first.StatusCode)
	}
	// Answering an already-answered interaction -> 400 (not pending).
	second := do(t, s, http.MethodPost,
		"/v1/jobs/"+jobID+"/interactions/"+created.ID+"/answer", testToken,
		answerInteractionReq{Answer: "b"})
	if second.StatusCode != http.StatusBadRequest {
		t.Fatalf("second answer status=%d, want 400", second.StatusCode)
	}
}

// TestInteractionCreateOnTerminalJob asserts you cannot raise an interaction on
// a finished job. After SP3 a terminal job is evicted from the in-memory map, but
// the service consults the metadata store on a memory miss and still reports it
// as terminal (409 Conflict) deterministically — never racing eviction into a
// spurious 404.
func TestInteractionCreateOnTerminalJob(t *testing.T) {
	s := newTestServer(t, testToken, false)
	// A short job that finishes quickly -> terminal.
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

	// Creating an interaction on a terminal (evicted) job -> 409 Conflict.
	createResp := do(t, s, http.MethodPost, "/v1/jobs/"+created.ID+"/interactions", testToken, createInteractionReq{
		Type: job.InteractionTypeQuestion, Prompt: "q",
	})
	if createResp.StatusCode != http.StatusConflict {
		t.Fatalf("create on terminal job status=%d, want 409", createResp.StatusCode)
	}
}
