package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// createWakeupJob submits a finished exec job (the target a wakeup resumes) and
// returns it. Exec keeps the test independent of any agent CLI.
func createWakeupJob(t *testing.T, s *Server, token string) job.JobResult {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", token, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create job status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	final, ok := s.jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("job %s never finished", created.ID)
	}
	return final
}

// hasWakeup reports whether the listing carries a wakeup id.
func hasWakeup(ws []wakeupView, id string) bool {
	for _, w := range ws {
		if w.ID == id {
			return true
		}
	}
	return false
}

// TestWakeupEndpoints covers the JOB-09 HTTP surface end to end: register (all four
// kinds), list, read, toggle, delete — plus the rejections that keep the surface
// honest (unknown job/wakeup 404, a bad spec 400, a worker caller 403).
func TestWakeupEndpoints(t *testing.T) {
	s := newTestServer(t, testToken, false)
	target := createWakeupJob(t, s, testToken)

	// Register an event subscription: the shape an agent uses from inside a job.
	resp := do(t, s, http.MethodPost, "/v1/jobs/"+target.ID+"/wakeups", testToken, job.WakeupSpec{
		Kind: jobstore.WakeupKindEvent, EventTypes: []string{job.EventJobTerminal},
		FilterStatus: []string{job.StatusDone}, Instruction: "merge the results",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create wakeup status=%d, want 200", resp.StatusCode)
	}
	var event wakeupView
	decode(t, resp, &event)
	if event.ID == "" || event.JobID != target.ID || event.Kind != jobstore.WakeupKindEvent {
		t.Fatalf("wakeup view = %+v, want an event wakeup on %s", event, target.ID)
	}
	if !event.Enabled || event.Mode != jobstore.WakeupModeOnce {
		t.Fatalf("wakeup view = %+v, want enabled and once by default", event)
	}
	if event.FilterJobID != target.ID {
		t.Fatalf("filter_job_id = %q, want the job it was registered on", event.FilterJobID)
	}
	if len(event.EventTypes) != 1 || event.EventTypes[0] != job.EventJobTerminal {
		t.Fatalf("event_types = %v, want the decoded array", event.EventTypes)
	}
	if len(event.FilterStatus) != 1 || event.FilterStatus[0] != job.StatusDone {
		t.Fatalf("filter_status = %v, want the decoded array", event.FilterStatus)
	}

	// A timer, to prove the wire carries the instant/interval fields too.
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+target.ID+"/wakeups", testToken, job.WakeupSpec{
		Kind: jobstore.WakeupKindEvery, EverySec: 3600, Instruction: "poll the queue",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create every wakeup status=%d, want 200", resp.StatusCode)
	}
	var every wakeupView
	decode(t, resp, &every)
	if every.EverySec != 3600 || every.NextRunAt == 0 || every.Mode != jobstore.WakeupModeContinuous {
		t.Fatalf("every wakeup = %+v, want an armed continuous timer", every)
	}

	// List returns both, oldest first.
	resp = do(t, s, http.MethodGet, "/v1/jobs/"+target.ID+"/wakeups", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d, want 200", resp.StatusCode)
	}
	var list wakeupsResp
	decode(t, resp, &list)
	// Both rows land in the same second here, so the store's id tiebreaker decides
	// their order — assert membership, not sequence (oldest-first is what the store
	// guarantees across seconds).
	if len(list.Wakeups) != 2 || !hasWakeup(list.Wakeups, event.ID) || !hasWakeup(list.Wakeups, every.ID) {
		t.Fatalf("list = %+v, want both wakeups", list.Wakeups)
	}

	// Read one back by id.
	resp = do(t, s, http.MethodGet, "/v1/wakeups/"+event.ID, testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status=%d, want 200", resp.StatusCode)
	}
	var one wakeupView
	decode(t, resp, &one)
	if one.ID != event.ID || one.Instruction != "merge the results" {
		t.Fatalf("get = %+v, want the registered wakeup", one)
	}

	// Disable, then enable: the switch is the only mutable field.
	resp = do(t, s, http.MethodPatch, "/v1/wakeups/"+event.ID, testToken, map[string]bool{"enabled": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disable status=%d, want 200", resp.StatusCode)
	}
	var off wakeupView
	decode(t, resp, &off)
	if off.Enabled {
		t.Fatalf("wakeup after disable = %+v, want it off", off)
	}
	resp = do(t, s, http.MethodPatch, "/v1/wakeups/"+event.ID, testToken, map[string]bool{"enabled": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enable status=%d, want 200", resp.StatusCode)
	}
	var on wakeupView
	decode(t, resp, &on)
	if !on.Enabled {
		t.Fatalf("wakeup after enable = %+v, want it on", on)
	}

	// Delete removes it, and it is gone from the listing.
	resp = do(t, s, http.MethodDelete, "/v1/wakeups/"+event.ID, testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status=%d, want 200", resp.StatusCode)
	}
	if resp = do(t, s, http.MethodGet, "/v1/wakeups/"+event.ID, testToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete status=%d, want 404", resp.StatusCode)
	}
	resp = do(t, s, http.MethodGet, "/v1/jobs/"+target.ID+"/wakeups", testToken, nil)
	decode(t, resp, &list)
	if len(list.Wakeups) != 1 || !hasWakeup(list.Wakeups, every.ID) {
		t.Fatalf("list after delete = %+v, want only the timer", list.Wakeups)
	}

	// Rejections: unknown ids are 404, a bad spec is 400.
	if resp = do(t, s, http.MethodPost, "/v1/jobs/no-such-job/wakeups", testToken,
		job.WakeupSpec{Kind: jobstore.WakeupKindAt, At: 4102444800}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("create on an unknown job status=%d, want 404", resp.StatusCode)
	}
	if resp = do(t, s, http.MethodGet, "/v1/jobs/no-such-job/wakeups", testToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("list of an unknown job status=%d, want 404", resp.StatusCode)
	}
	if resp = do(t, s, http.MethodGet, "/v1/wakeups/nope", testToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get of an unknown wakeup status=%d, want 404", resp.StatusCode)
	}
	if resp = do(t, s, http.MethodPost, "/v1/jobs/"+target.ID+"/wakeups", testToken,
		job.WakeupSpec{Kind: jobstore.WakeupKindAt, At: 1}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create with a past instant status=%d, want 400", resp.StatusCode)
	}
	if resp = do(t, s, http.MethodPost, "/v1/jobs/"+target.ID+"/wakeups", testToken,
		job.WakeupSpec{Kind: jobstore.WakeupKindEvent, EventTypes: []string{"job.exploded"}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create with an unknown event type status=%d, want 400", resp.StatusCode)
	}
}

// TestWakeupEndpointsRejectWorkerCaller: a worker token is an executing machine, and
// a wakeup starts new work as a named caller, so it may not register or switch one
// (reads stay open, like every other job metadata read).
func TestWakeupEndpointsRejectWorkerCaller(t *testing.T) {
	const workerToken = "worker-secret"
	s := newTestServerCfg(t, config.ServerConfig{
		Token:   testToken,
		Workers: map[string]config.WorkerAuthConfig{"w-1": {Token: workerToken}},
	})
	target := createWakeupJob(t, s, testToken)
	w, err := s.jobs.CreateWakeup(job.WakeupSpec{
		JobID: target.ID, Kind: jobstore.WakeupKindAt, At: 4102444800,
	}, "default")
	if err != nil {
		t.Fatalf("CreateWakeup: %v", err)
	}

	body, err := json.Marshal(job.WakeupSpec{Kind: jobstore.WakeupKindAt, At: 4102444800})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp := do(t, s, http.MethodPost, "/v1/jobs/"+target.ID+"/wakeups", workerToken, json.RawMessage(body))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("worker create status=%d, want 403", resp.StatusCode)
	}
	if resp = do(t, s, http.MethodPatch, "/v1/wakeups/"+w.ID, workerToken, map[string]bool{"enabled": false}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("worker patch status=%d, want 403", resp.StatusCode)
	}
	if resp = do(t, s, http.MethodDelete, "/v1/wakeups/"+w.ID, workerToken, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("worker delete status=%d, want 403", resp.StatusCode)
	}
	// Reading is not gated: a worker mirrors the job it runs, wakeups included.
	if resp = do(t, s, http.MethodGet, "/v1/jobs/"+target.ID+"/wakeups", workerToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("worker list status=%d, want 200", resp.StatusCode)
	}
}
