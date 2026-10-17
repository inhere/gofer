package httpapi

import (
	"io"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// submitTitledHeld posts a held exec job carrying a title and an explicit command and
// returns the decoded submit body.
func submitTitledHeld(t *testing.T, s *Server, token, title string, cmd []string) submitResponse {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", token, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: cmd, Cwd: ".", TimeoutSec: 30, Title: title,
		Hold: true, HoldReason: "push the release branch",
	})
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("held submit = %d %s, want 202", resp.StatusCode, b)
	}
	var out submitResponse
	decode(t, resp, &out)
	if out.Status != job.StatusAwaitingApproval || out.Hold == nil {
		t.Fatalf("held submit body = %+v", out)
	}
	return out
}

// heldApprovals reads the bell's pending payload and returns its approvals.
func heldApprovals(t *testing.T, s *Server) ([]approvalView, map[string]any) {
	t.Helper()
	snap, err := s.pendingSnapshot()
	if err != nil {
		t.Fatalf("pendingSnapshot: %v", err)
	}
	m, ok := snap.(map[string]any)
	if !ok {
		t.Fatalf("pending snapshot shape = %T, want a map", snap)
	}
	list, ok := m["approvals"].([]approvalView)
	if !ok {
		t.Fatalf("no approvals in the pending snapshot: %#v", m)
	}
	return list, m
}

// TestPendingSnapshotCarriesHeldJobs (gofer-5wvc): the bell's pending payload carries the
// jobs waiting in awaiting_approval — id, title, project, since when they wait, the hold
// deadline and the command the approval is about (deciding means reading it) — and a
// decided hold leaves it again. The interactions / decisions the bell always had stay.
func TestPendingSnapshotCarriesHeldJobs(t *testing.T) {
	t.Parallel()
	s := newHoldHTTPServer(t)

	titled := submitTitledHeld(t, s, "tok-alice", "run the suite before the release", []string{"go", "test", "./..."})
	untitled := submitHeld(t, s, "tok-alice")

	list, m := heldApprovals(t, s)
	if len(list) != 2 {
		t.Fatalf("approvals = %+v, want both held jobs", list)
	}
	byID := map[string]approvalView{}
	for _, a := range list {
		byID[a.ID] = a
	}
	got, ok := byID[titled.ID]
	if !ok {
		t.Fatalf("held job %s missing from the snapshot: %+v", titled.ID, list)
	}
	if got.Title != "run the suite before the release" || got.ProjectKey != "self" {
		t.Fatalf("approval = %+v, want its title and project", got)
	}
	if got.WaitingSince != titled.StartedAt || got.ExpiresAt != titled.Hold.ExpiresAt {
		t.Fatalf("approval times = started %d expires %d, want %d / %d",
			got.WaitingSince, got.ExpiresAt, titled.StartedAt, titled.Hold.ExpiresAt)
	}
	if len(got.HoldCmd) != 3 || got.HoldCmd[0] != "go" || got.HoldCmd[2] != "./..." {
		t.Fatalf("hold_cmd = %v, want the command being approved", got.HoldCmd)
	}
	// The submit path derives a title from the command when the caller names none, so the
	// second card carries that one.
	if bare, ok := byID[untitled.ID]; !ok || bare.Title != "go version" || bare.ExpiresAt != untitled.Hold.ExpiresAt {
		t.Fatalf("second approval = %+v (present=%v)", bare, ok)
	}

	resp := do(t, s, http.MethodPost, "/v1/jobs/"+untitled.ID+"/reject", "tok-alice", map[string]string{"reason": "wrong branch"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reject = %d, want 200", resp.StatusCode)
	}
	list, m = heldApprovals(t, s)
	if len(list) != 1 || list[0].ID != titled.ID {
		t.Fatalf("decided hold still in the snapshot: %+v", list)
	}
	if _, ok := m["interactions"]; !ok {
		t.Fatalf("the pending payload lost its interactions: %#v", m)
	}
	if _, ok := m["decisions"]; !ok {
		t.Fatalf("the pending payload lost its decisions: %#v", m)
	}
}
