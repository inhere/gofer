package httpapi

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// runExecJob submits an exec job through the Service and waits for it to finish,
// returning the terminal result. Used to seed a job (and its events) for the
// events endpoint tests.
func runExecJob(t *testing.T, s *Server, cmd []string) job.JobResult {
	t.Helper()
	res, err := s.jobs.Submit(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: cmd, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final, ok := s.jobs.Wait(res.ID)
	if !ok {
		t.Fatalf("Wait: job %s not found", res.ID)
	}
	return final
}

// TestListEventsOrdered asserts GET /v1/jobs/{id}/events returns the lifecycle
// events in seq order for a finished job (submitted -> running -> terminal).
func TestListEventsOrdered(t *testing.T) {
	s := newTestServer(t, testToken, false)
	final := runExecJob(t, s, []string{"go", "version"})
	if final.Status != job.StatusDone {
		t.Fatalf("setup: status=%q, want done", final.Status)
	}

	resp := do(t, s, http.MethodGet, "/v1/jobs/"+final.ID+"/events", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Events []jobstore.JobEvent `json:"events"`
	}
	decode(t, resp, &body)
	if len(body.Events) < 3 {
		t.Fatalf("expected >=3 events, got %d (%+v)", len(body.Events), body.Events)
	}
	// seq strictly increasing.
	for i := 1; i < len(body.Events); i++ {
		if body.Events[i].Seq <= body.Events[i-1].Seq {
			t.Fatalf("events not in seq order: %+v", body.Events)
		}
	}
	// First is submitted, last is terminal.
	if body.Events[0].Type != job.EventJobSubmitted {
		t.Fatalf("first event=%q, want %q", body.Events[0].Type, job.EventJobSubmitted)
	}
	if last := body.Events[len(body.Events)-1]; last.Type != job.EventJobTerminal {
		t.Fatalf("last event=%q, want %q", last.Type, job.EventJobTerminal)
	}
}

// TestListEventsSince asserts ?since=<seq> returns only events after the cursor.
func TestListEventsSince(t *testing.T) {
	s := newTestServer(t, testToken, false)
	final := runExecJob(t, s, []string{"go", "version"})

	resp := do(t, s, http.MethodGet, "/v1/jobs/"+final.ID+"/events", testToken, nil)
	var all struct {
		Events []jobstore.JobEvent `json:"events"`
	}
	decode(t, resp, &all)
	if len(all.Events) < 2 {
		t.Fatalf("setup: need >=2 events, got %d", len(all.Events))
	}
	firstSeq := all.Events[0].Seq

	resp2 := do(t, s, http.MethodGet, "/v1/jobs/"+final.ID+"/events?since="+strconv.FormatInt(firstSeq, 10), testToken, nil)
	var since struct {
		Events []jobstore.JobEvent `json:"events"`
	}
	decode(t, resp2, &since)
	if len(since.Events) != len(all.Events)-1 {
		t.Fatalf("since=%d returned %d events, want %d", firstSeq, len(since.Events), len(all.Events)-1)
	}
	for _, ev := range since.Events {
		if ev.Seq <= firstSeq {
			t.Fatalf("since result contains seq<=cursor: %+v", ev)
		}
	}
}

// TestListEventsUnknownJob asserts an unknown id is a 404.
func TestListEventsUnknownJob(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/jobs/does-not-exist/events", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}
