package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// runningSleepJob starts a long-lived exec job so interactions can be raised while it is live.
func runningSleepJob(t *testing.T, s *Server) job.JobResult {
	t.Helper()
	res, err := s.jobs.Submit(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "sleep", "30s"), Cwd: ".", TimeoutSec: 60,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	t.Cleanup(func() { _ = s.jobs.Cancel(res.ID); s.jobs.Wait(res.ID) })
	wait.Until(t, 10*time.Second, "job running", func() bool {
		r, ok := s.jobs.Get(res.ID)
		return ok && r.Status == job.StatusRunning
	})
	return res
}

// A job credential opens and reads interactions on ITS OWN job (gofer_ask), never on another
// job, and never an approval-gate (permission) one; answering stays a person's.
func TestJobCredentialInteractionsOwnJobOnly(t *testing.T) {
	t.Parallel()
	s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: "tok-user"}}},
		map[string]config.AgentConfig{"exec": {Type: agent.TypeExec}}, nil)
	mine := runningSleepJob(t, s)
	other := runningSleepJob(t, s)
	tok := seedJobToken(t, s, mine.ID, jobstore.JobCredentialMember, "")

	// Own job: a choice with the gofer_ask extension, then read it back.
	resp := do(t, s, http.MethodPost, "/v1/jobs/"+mine.ID+"/interactions", tok, map[string]any{
		"type": "choice", "prompt": "which?", "multi": true, "allow_other": true, "recommended": "a", "timeout_sec": 600,
		"options": []map[string]any{{"value": "a", "label": "a"}, {"value": "b", "label": "b"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own-job interaction = %d, want 200", resp.StatusCode)
	}
	var it job.Interaction
	decode(t, resp, &it)
	if it.JobID != mine.ID || !it.Multi || !it.AllowOther || it.Recommended != "a" || it.ExpiresAt == 0 {
		t.Fatalf("created interaction = %+v", it)
	}
	resp = do(t, s, http.MethodGet, "/v1/jobs/"+mine.ID+"/interactions", tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own-job interactions read = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// Another job: both directions refused with the credential wording.
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+other.ID+"/interactions", tok, map[string]any{"type": "question", "prompt": "hi"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("interaction on another job = %d, want 403", resp.StatusCode)
	}
	assertJobCredentialRefusal(t, resp)
	resp = do(t, s, http.MethodGet, "/v1/jobs/"+other.ID+"/interactions", tok, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reading another job's interactions = %d, want 403", resp.StatusCode)
	}
	assertJobCredentialRefusal(t, resp)
	if list, _ := s.jobs.GetInteractions(other.ID); len(list) != 0 {
		t.Fatalf("nothing may land on another job: %+v", list)
	}

	// A permission interaction is the runner's to raise, not the agent's.
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+mine.ID+"/interactions", tok, map[string]any{"type": "permission", "prompt": "allow?"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("permission interaction by a job credential = %d, want 403", resp.StatusCode)
	}
	assertJobCredentialRefusal(t, resp)

	// Answering its own question stays a person's.
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+mine.ID+"/interactions/"+it.ID+"/answer", tok, map[string]any{"answer": `["a"]`})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("self-answer by a job credential = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}
