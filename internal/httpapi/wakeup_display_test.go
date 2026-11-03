package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// seedWakeup stores a wakeup row directly: the display cases need a row in a state only
// the receipt path produces (the pending-delivery sentinel), without running a session.
func seedWakeup(t *testing.T, s *Server, w jobstore.WakeupRecord) {
	t.Helper()
	if w.ID == "" {
		w.ID = "wk-" + job.RandomSuffix()
	}
	if w.JobID == "" {
		w.JobID = "job-wake"
	}
	if w.Kind == "" {
		w.Kind = jobstore.WakeupKindEvent
	}
	if w.Mode == "" {
		w.Mode = jobstore.WakeupModeContinuous
	}
	if w.EventTypesJSON == "" {
		w.EventTypesJSON = jobstore.EncodeStringList([]string{job.EventJobTerminal})
	}
	w.Enabled = 1
	w.CreatedAt = time.Now().Unix()
	if err := s.jobs.Meta().InsertWakeup(w); err != nil {
		t.Fatalf("insert wakeup %s: %v", w.ID, err)
	}
}

// TestWakeupPendingDeliveryIsShownAsPending: the pending-delivery sentinel is NOT a job id.
// The wakeup API reports it as pending_delivery and leaves continuation_job_id empty, so no
// client can print `await_input` where a job id belongs.
func TestWakeupPendingDeliveryIsShownAsPending(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	target := createWakeupJob(t, s, testToken)
	seedWakeup(t, s, jobstore.WakeupRecord{
		JobID: target.ID, FilterJobID: target.ID, ContinuationJobID: jobstore.WakeupPendingSay,
	})

	resp := do(t, s, http.MethodGet, "/v1/jobs/"+target.ID+"/wakeups", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list wakeups status=%d, want 200", resp.StatusCode)
	}
	var list wakeupsResp
	decode(t, resp, &list)
	if len(list.Wakeups) != 1 {
		t.Fatalf("wakeups = %+v, want one", list.Wakeups)
	}
	got := list.Wakeups[0]
	if !got.PendingDelivery {
		t.Errorf("pending_delivery = false, want true for a sentinel row")
	}
	if got.ContinuationJobID != "" {
		t.Errorf("continuation_job_id = %q, want it empty (the sentinel is not a job)", got.ContinuationJobID)
	}

	// The single-wakeup read answers the same way.
	one := do(t, s, http.MethodGet, "/v1/wakeups/"+got.ID, testToken, nil)
	if one.StatusCode != http.StatusOK {
		t.Fatalf("get wakeup status=%d, want 200", one.StatusCode)
	}
	var single wakeupView
	decode(t, one, &single)
	if !single.PendingDelivery || single.ContinuationJobID != "" {
		t.Errorf("single wakeup = %+v, want pending delivery and no job id", single)
	}

	// A live claim stays masked exactly as before, and is not reported as a delivery.
	seedWakeup(t, s, jobstore.WakeupRecord{
		ID: "wk-claim", JobID: target.ID, FilterJobID: target.ID,
		ContinuationJobID: jobstore.WakeupClaimPending,
	})
	claim := do(t, s, http.MethodGet, "/v1/wakeups/wk-claim", testToken, nil)
	var claimView wakeupView
	decode(t, claim, &claimView)
	if claimView.PendingDelivery || claimView.ContinuationJobID != "" {
		t.Errorf("in-flight claim = %+v, want neither a job id nor a pending delivery", claimView)
	}
}

// seedResidentJob stores a row that reads back as a LIVE resident job: a seeded worker
// session state plus a non-finished status is what Service.Get projects from the store.
func seedResidentJob(t *testing.T, s *Server, id, caller, status string) {
	t.Helper()
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: id, ProjectKey: "self", Agent: "codex", Runner: "local", Status: status,
		Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, UpdatedAt: now,
		CallerID: caller, SessionStateJSON: `{"session":true,"turn_no":1}`,
	}); err != nil {
		t.Fatalf("seed resident job %s: %v", id, err)
	}
}

// TestWakeupEventOnLiveResidentNeedsManageOthers: a manual EVENT wakeup on a live resident
// job is delivered as a say — its instruction is spoken into that session — so it must
// pass the same rule `job say` passes. job.wake.others alone may not inject text into
// someone else's session; a timer wakeup (which resumes, not speaks) and a finished
// resident job keep the old rule.
func TestWakeupEventOnLiveResidentNeedsManageOthers(t *testing.T) {
	// Not parallel: setDBGrants swaps the server's grant cache.
	s := permissionOnlyServer(t)
	seedResidentJob(t, s, "live-session", "bob", job.StatusAwaitingInput)
	seedResidentJob(t, s, "ended-session", "bob", job.StatusDone)
	spec := job.WakeupSpec{
		Kind:         jobstore.WakeupKindEvent,
		EventTypes:   []string{job.EventJobTerminal},
		Instruction:  "look at the queue",
		FilterJobID:  "live-session",
		FilterStatus: nil,
	}

	wakeOnly := []string{authz.PermJobWakeOthers, authz.PermJobSubmit}
	s.setDBGrants(grantsFor(wakeOnly))
	resp := do(t, s, http.MethodPost, "/v1/jobs/live-session/wakeups", "tok-alice", spec)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("event wakeup on a live session with job.wake.others only = %d, want 403 (body %s)",
			resp.StatusCode, body)
	}
	if !strings.Contains(body, authz.PermJobManageOthers) {
		t.Errorf("403 body %q does not name %s", body, authz.PermJobManageOthers)
	}

	// With the say rule satisfied it goes through.
	s.setDBGrants(grantsFor(append(wakeOnly, authz.PermJobManageOthers)))
	resp = do(t, s, http.MethodPost, "/v1/jobs/live-session/wakeups", "tok-alice", spec)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("event wakeup with job.manage.others = %d, want 200 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	_ = resp.Body.Close()

	// A timer wakeup does not speak into the session, so job.wake.others is enough.
	s.setDBGrants(grantsFor(wakeOnly))
	timer := job.WakeupSpec{Kind: jobstore.WakeupKindAt, At: time.Now().Add(time.Hour).Unix(), Instruction: "later"}
	resp = do(t, s, http.MethodPost, "/v1/jobs/live-session/wakeups", "tok-alice", timer)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("timer wakeup with job.wake.others = %d, want 200 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	_ = resp.Body.Close()

	// A resident job that already ended is resumed, not spoken to: the old rule stands.
	ended := spec
	ended.FilterJobID = "ended-session"
	resp = do(t, s, http.MethodPost, "/v1/jobs/ended-session/wakeups", "tok-alice", ended)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("event wakeup on an ended session with job.wake.others = %d, want 200 (body %s)",
			resp.StatusCode, readBody(t, resp))
	}
	_ = resp.Body.Close()
}
