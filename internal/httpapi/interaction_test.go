package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/answerguard"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

type interactionRoleStub map[string]string

func (r interactionRoleStub) Role(id string) (string, bool) { v, ok := r[id]; return v, ok }

const (
	interactionGateOwner = "agt_owner"
	interactionGateSup   = "agt_sup"
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

func waitRunningTok(t *testing.T, s *Server, id, token string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp := do(t, s, http.MethodGet, "/v1/jobs/"+id, token, nil)
		var jr job.JobResult
		decode(t, resp, &jr)
		if jr.Status == job.StatusRunning || jr.Status == job.StatusPendingInteraction {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach running in time", id)
}

func submitRunningJobTok(t *testing.T, s *Server, token, originAgent string) string {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", token, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "30"}, Cwd: ".", TimeoutSec: 60,
		OriginAgent: originAgent,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit running job: status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatalf("running job has no id: %+v", created)
	}
	waitRunningTok(t, s, created.ID, token)
	t.Cleanup(func() {
		do(t, s, http.MethodPost, "/v1/jobs/"+created.ID+"/cancel", token, nil)
	})
	return created.ID
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

func TestInteractionAnswerUsesAuthenticatedCallerForHumanPath(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	s.jobs.SetAnswerGuard(answerguard.New([]string{"^pick "}, interactionRoleStub{interactionGateOwner: "", interactionGateSup: "supervisor"}))
	jobID := submitRunningJobTok(t, s, "tok-alice", interactionGateOwner)

	createResp := do(t, s, http.MethodPost, "/v1/jobs/"+jobID+"/interactions", "tok-alice", createInteractionReq{
		Type: job.InteractionTypeConfirmation, Prompt: "delete prod?",
	})
	var created job.Interaction
	decode(t, createResp, &created)

	agentResp := do(t, s, http.MethodPost,
		"/v1/jobs/"+jobID+"/interactions/"+created.ID+"/answer", "tok-alice",
		answerInteractionReq{Answer: "yes", Responder: interactionGateSup})
	if agentResp.StatusCode != http.StatusForbidden {
		t.Fatalf("supervisor responder status=%d, want 403", agentResp.StatusCode)
	}
	agentResp.Body.Close()

	ansResp := do(t, s, http.MethodPost,
		"/v1/jobs/"+jobID+"/interactions/"+created.ID+"/answer", "tok-alice",
		answerInteractionReq{Answer: "yes"})
	if ansResp.StatusCode != http.StatusOK {
		t.Fatalf("human answer status=%d, want 200", ansResp.StatusCode)
	}
	var answered job.Interaction
	decode(t, ansResp, &answered)
	if answered.AnsweredBy != "alice" {
		t.Fatalf("answered_by=%q, want alice", answered.AnsweredBy)
	}
}

func TestInteractionAnswerEmptyCallerFallsBackToHuman(t *testing.T) {
	s := newTestServer(t, "", true)
	jobID := submitRunningJobTok(t, s, "", "")

	createResp := do(t, s, http.MethodPost, "/v1/jobs/"+jobID+"/interactions", "", createInteractionReq{
		Type: job.InteractionTypeQuestion, Prompt: "continue?",
	})
	var created job.Interaction
	decode(t, createResp, &created)

	ansResp := do(t, s, http.MethodPost,
		"/v1/jobs/"+jobID+"/interactions/"+created.ID+"/answer", "",
		answerInteractionReq{Answer: "ok"})
	if ansResp.StatusCode != http.StatusOK {
		t.Fatalf("answer status=%d, want 200", ansResp.StatusCode)
	}
	var answered job.Interaction
	decode(t, ansResp, &answered)
	if answered.AnsweredBy != "human" {
		t.Fatalf("answered_by=%q, want human", answered.AnsweredBy)
	}
}

func TestInteractionAgentResponderPathStillGatedAndPrefixed(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	s.jobs.SetAnswerGuard(answerguard.New([]string{"^pick "}, interactionRoleStub{interactionGateOwner: "", interactionGateSup: "supervisor"}))
	jobID := submitRunningJobTok(t, s, "tok-alice", interactionGateOwner)

	blockedResp := do(t, s, http.MethodPost, "/v1/jobs/"+jobID+"/interactions", "tok-alice", createInteractionReq{
		Type: job.InteractionTypeConfirmation, Prompt: "delete prod?",
	})
	var blocked job.Interaction
	decode(t, blockedResp, &blocked)
	denied := do(t, s, http.MethodPost,
		"/v1/jobs/"+jobID+"/interactions/"+blocked.ID+"/answer", "tok-alice",
		answerInteractionReq{Answer: "yes", Responder: interactionGateSup})
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("supervisor confirmation status=%d, want 403", denied.StatusCode)
	}
	denied.Body.Close()

	allowedResp := do(t, s, http.MethodPost, "/v1/jobs/"+jobID+"/interactions", "tok-alice", createInteractionReq{
		Type: job.InteractionTypeChoice, Prompt: "pick one format",
		Options: []job.InteractionOption{{Value: "json"}, {Value: "yaml"}},
	})
	var allowed job.Interaction
	decode(t, allowedResp, &allowed)
	ok := do(t, s, http.MethodPost,
		"/v1/jobs/"+jobID+"/interactions/"+allowed.ID+"/answer", "tok-alice",
		answerInteractionReq{Answer: "json", Responder: interactionGateSup})
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("supervisor whitelisted choice status=%d, want 200", ok.StatusCode)
	}
	var answered job.Interaction
	decode(t, ok, &answered)
	if answered.AnsweredBy != "agent:"+interactionGateSup {
		t.Fatalf("agent answered_by=%q, want agent:%s", answered.AnsweredBy, interactionGateSup)
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

// TestInteractionPunt exercises the punt endpoint (y5wt, the sup's client→serve path): it
// flips needs_human on a pending interaction, leaves it pending, and is an idempotent no-op
// (200) for an unknown interaction id (targeted update touches 0 rows).
func TestInteractionPunt(t *testing.T) {
	s := newTestServer(t, testToken, false)
	jobID := submitRunningJob(t, s)

	createResp := do(t, s, http.MethodPost, "/v1/jobs/"+jobID+"/interactions", testToken, createInteractionReq{
		Type: job.InteractionTypeConfirmation, Prompt: "delete prod?",
	})
	var created job.Interaction
	decode(t, createResp, &created)

	puntResp := do(t, s, http.MethodPost,
		"/v1/jobs/"+jobID+"/interactions/"+created.ID+"/punt", testToken, nil)
	if puntResp.StatusCode != http.StatusOK {
		t.Fatalf("punt status=%d, want 200", puntResp.StatusCode)
	}

	// Still pending, now flagged needs_human.
	listResp := do(t, s, http.MethodGet, "/v1/jobs/"+jobID+"/interactions", testToken, nil)
	var list struct {
		Interactions []job.Interaction `json:"interactions"`
	}
	decode(t, listResp, &list)
	if len(list.Interactions) != 1 || list.Interactions[0].Status != job.InteractionPending {
		t.Fatalf("punted interaction should stay pending: %+v", list.Interactions)
	}
	if list.Interactions[0].NeedsHuman != 1 {
		t.Fatalf("expected needs_human=1 after punt, got %+v", list.Interactions[0])
	}
	eventsResp := do(t, s, http.MethodGet, "/v1/jobs/"+jobID+"/events", testToken, nil)
	var eventsBody struct {
		Events []jobstore.JobEvent `json:"events"`
	}
	decode(t, eventsResp, &eventsBody)
	var puntDetail map[string]string
	for _, ev := range eventsBody.Events {
		if ev.Type == job.EventInteractionPunted {
			if err := json.Unmarshal([]byte(ev.Detail), &puntDetail); err != nil {
				t.Fatalf("unmarshal punt detail %q: %v", ev.Detail, err)
			}
			break
		}
	}
	if puntDetail["interaction_id"] != created.ID || puntDetail["caller_id"] != "default" {
		t.Fatalf("punt event detail=%v, want interaction_id=%s caller_id=default", puntDetail, created.ID)
	}

	// Idempotent no-op for an unknown interaction id.
	ghost := do(t, s, http.MethodPost,
		"/v1/jobs/"+jobID+"/interactions/ghost/punt", testToken, nil)
	if ghost.StatusCode != http.StatusOK {
		t.Fatalf("punt unknown interaction status=%d, want 200 (no-op)", ghost.StatusCode)
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

// TestListPendingInteractionsEndpoint verifies GET /v1/interactions?status=pending
// returns a live job's pending interaction (cross-job supervisor discovery, E25)
// and rejects an unsupported status filter.
func TestListPendingInteractionsEndpoint(t *testing.T) {
	s := newTestServer(t, testToken, false)
	jobID := submitRunningJob(t, s)

	// Raise a pending interaction on the running job.
	cr := do(t, s, http.MethodPost, "/v1/jobs/"+jobID+"/interactions", testToken, createInteractionReq{
		Type: job.InteractionTypeQuestion, Prompt: "continue?",
	})
	if cr.StatusCode != http.StatusOK {
		t.Fatalf("create interaction status=%d", cr.StatusCode)
	}

	// The cross-job pending list includes it (carrying its job_id).
	resp := do(t, s, http.MethodGet, "/v1/interactions?status=pending", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list pending status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Interactions []job.Interaction `json:"interactions"`
	}
	decode(t, resp, &body)
	var found bool
	for _, it := range body.Interactions {
		if it.JobID == jobID && it.Status == job.InteractionPending {
			found = true
		}
	}
	if !found {
		t.Fatalf("pending interaction for job %s not in list: %+v", jobID, body.Interactions)
	}

	// An unsupported status filter is a 400.
	bad := do(t, s, http.MethodGet, "/v1/interactions?status=answered", testToken, nil)
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=answered should be 400, got %d", bad.StatusCode)
	}
	bad.Body.Close()
}
