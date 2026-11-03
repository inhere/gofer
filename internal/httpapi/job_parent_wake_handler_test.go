package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// T6 at the HTTP entry: the parent of a child receipt is the job whose CREDENTIAL
// submitted the child, never a value from the body, and the two receipt switches are a
// job credential's alone.

// seedResidentParent stores a live resident session job row (that is what makes a job a
// receipt target) and returns its id.
func seedResidentParent(t *testing.T, s *Server, id, callerID string) string {
	t.Helper()
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: id, ProjectKey: "self", Agent: "exec", Runner: "local", Status: job.StatusRunning,
		Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, UpdatedAt: now, CallerID: callerID,
		// The session flag lives in session_state_json (there is no column for it).
		SessionStateJSON: `{"session":true}`,
	}); err != nil {
		t.Fatalf("seed resident parent %s: %v", id, err)
	}
	return id
}

// TestChildWakeParentComesFromTheCredential: a job credential's submit registers its
// receipt on ITS job, and a parent_job_id in the body changes nothing — the field is not
// on the wire, so a body can never nominate somebody else's job.
func TestChildWakeParentComesFromTheCredential(t *testing.T) {
	t.Parallel()
	const userTok = "tok-user"
	s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: userTok}}},
		map[string]config.AgentConfig{"exec": {Type: agent.TypeExec, CanSubmit: true, SubmitAgents: []string{"exec"}}}, nil)

	parent := seedResidentParent(t, s, "job-parent", "alice")
	other := seedResidentParent(t, s, "job-other", "alice")
	tok := seedJobToken(t, s, parent, jobstore.JobCredentialMember, "")

	resp := do(t, s, http.MethodPost, "/v1/jobs", tok, map[string]any{
		"project_key": "self", "agent": "exec", "runner": "local",
		"cmd": []string{"go", "version"}, "cwd": ".", "timeout_sec": 60,
		// Forged: the parent is decided by the credential, not by this.
		"parent_job_id":      other,
		"parent_wake_events": []string{"job.terminal"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit = %d", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)

	rows, err := s.jobs.ListWakeups(parent)
	if err != nil {
		t.Fatalf("ListWakeups(parent): %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("receipts on the credential's own job = %d, want one", len(rows))
	}
	if rows[0].FilterJobID != created.ID || rows[0].AutoChild != 1 {
		t.Fatalf("receipt = %+v, want a child receipt for %s", rows[0], created.ID)
	}
	forged, err := s.jobs.ListWakeups(other)
	if err != nil {
		t.Fatalf("ListWakeups(other): %v", err)
	}
	if len(forged) != 0 {
		t.Fatalf("a body-forged parent got %d receipts", len(forged))
	}
}

// TestChildWakeOptionsRefusedForNonJobCallers: no_parent_wake / parent_wake_events
// describe a receipt only a job credential's submit can have, so any other caller that
// sends them is refused instead of being told nothing and believing a receipt exists.
func TestChildWakeOptionsRefusedForNonJobCallers(t *testing.T) {
	t.Parallel()
	const userTok = "tok-user"
	s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: userTok}}},
		map[string]config.AgentConfig{"exec": {Type: agent.TypeExec, CanSubmit: true}}, nil)

	for _, body := range []map[string]any{
		{"no_parent_wake": true},
		{"parent_wake_events": []string{"job.terminal"}},
	} {
		req := map[string]any{
			"project_key": "self", "agent": "exec", "runner": "local",
			"cmd": []string{"go", "version"}, "cwd": ".", "timeout_sec": 60,
		}
		for k, v := range body {
			req[k] = v
		}
		resp := do(t, s, http.MethodPost, "/v1/jobs", userTok, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%v as a user caller = %d, want 400", body, resp.StatusCode)
		}
	}
}
