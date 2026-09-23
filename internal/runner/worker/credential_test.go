package worker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/wsproto"
)

// TestWorkerDispatchCarriesJobToken / TestOldWorkerGetsNoJobToken are the SEC-01
// dispatch negotiation: the hub's credential for a job reaches a worker that speaks
// the credential protocol, and MUST NOT be sent to one that does not — a peer that
// cannot use it would either drop the key (running the job without a credential) or,
// worse, keep a secret nobody asked it to hold.
func TestWorkerDispatchCarriesJobToken(t *testing.T) {
	h := &fakeHub{instanceID: "inst-1", workerProto: wsproto.JobCredentialMinProtocolVersion}
	r := newRunnerWithHub(h)

	events := map[string]map[string]any{}
	runDispatchToCompletion(t, r, "j1", runner.Forward{JobToken: "gjt_j1_deadbeef"}, events)

	d := h.dispatchedFrame()
	if d.JobToken != "gjt_j1_deadbeef" {
		t.Fatalf("dispatch job_token = %q, want the hub's credential", d.JobToken)
	}
	if _, skipped := events[runner.EventCredentialSkipped]; skipped {
		t.Fatalf("a credential-capable worker got %s", runner.EventCredentialSkipped)
	}
}

// TestOldWorkerGetsNoJobToken: a v10 worker cannot receive the credential, the job
// still runs, and the omission is recorded as job.credential_skipped so it is visible
// on the job's own timeline (the same shape as JOB-10's skills skip).
func TestOldWorkerGetsNoJobToken(t *testing.T) {
	h := &fakeHub{instanceID: "inst-1", workerProto: wsproto.JobCredentialMinProtocolVersion - 1}
	r := newRunnerWithHub(h)

	events := map[string]map[string]any{}
	runDispatchToCompletion(t, r, "j1", runner.Forward{JobToken: "gjt_j1_deadbeef"}, events)

	if d := h.dispatchedFrame(); d.JobToken != "" {
		t.Fatalf("dispatch to a v%d worker carried job_token %q", h.workerProto, d.JobToken)
	}
	detail, ok := events[runner.EventCredentialSkipped]
	if !ok {
		t.Fatalf("no %s event for the old worker (got %v)", runner.EventCredentialSkipped, events)
	}
	if detail["reason"] != "worker_protocol" {
		t.Fatalf("skip reason = %v, want worker_protocol", detail["reason"])
	}
	if d := h.dispatchedFrame(); d.JobID == "" {
		t.Fatal("the job was not dispatched at all: an old worker must still run it")
	}
}

// runDispatchToCompletion drives one dispatch through the runner to a terminal result,
// collecting the job events the runner raises on the way (OnJobEvent is how a skipped
// capability is reported).
func runDispatchToCompletion(t *testing.T, r *Runner, jobID string, f runner.Forward, events map[string]map[string]any) runner.Result {
	t.Helper()
	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(context.Background(), runner.Request{
			JobID:   jobID,
			Forward: &f,
			OnJobEvent: func(eventType string, detail map[string]any) {
				events[eventType] = detail
			},
		})
	}()
	hub, _ := r.hub.(*fakeHub)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && hub.getSink() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	sink := hub.getSink()
	if sink == nil {
		t.Fatal("sink never registered — the dispatch did not start")
	}
	sink.Finish(wsproto.Result{JobID: jobID, Status: "done", ExitCode: 0})
	select {
	case res := <-done:
		return res
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Finish")
		return runner.Result{}
	}
}

// TestJobTokenForNegotiation pins the pure negotiation helper the dispatch above leans
// on: an unknown protocol version is treated as "cannot carry" (the same conservative
// reading splitSkillUploads uses, and for the same reason — the cost of guessing wrong
// is a secret on a peer that cannot be trusted with it).
func TestJobTokenForNegotiation(t *testing.T) {
	cases := []struct {
		name  string
		proto int
		known bool
		token string
		want  string
	}{
		{"v11 carries", wsproto.JobCredentialMinProtocolVersion, true, "tok", "tok"},
		{"v10 does not", wsproto.JobCredentialMinProtocolVersion - 1, true, "tok", ""},
		{"unknown version does not", 11, false, "tok", ""},
		{"no token mints nothing", 11, true, "", ""},
	}
	for _, tc := range cases {
		if got := jobTokenFor(tc.proto, tc.known, tc.token); got != tc.want {
			t.Fatalf("%s: jobTokenFor = %q, want %q", tc.name, got, tc.want)
		}
	}
	if !strings.Contains(runner.EventCredentialSkipped, "credential") {
		t.Fatalf("EventCredentialSkipped = %q", runner.EventCredentialSkipped)
	}
}
