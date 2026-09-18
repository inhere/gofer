package worker_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/wsproto"
)

// TestWorkerVerifyOutcomeMirrored proves the verify step travels the WHOLE worker
// path (SUP-01 P2): the hub dispatches verify with the job, the WORKER runs it in
// its own checkout, and the structured result + the log banners come back over the
// Outcome/log frames onto the hub's job row.
//
// The banner count is part of the assertion: the hub must NOT re-run the step for a
// job it merely forwarded (a second run would append a second banner to the same
// stderr log the client reads).
func TestWorkerVerifyOutcomeMirrored(t *testing.T) {
	hub := buildHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	cl := buildWorkerSide(t, hub.ts.URL)
	clientErr := make(chan error, 1)
	go func() { clientErr <- cl.Run(ctx) }()
	waitWorkerOnline(t, hub.hub)

	bin := testcmd.Path(t)
	verify := []string{bin, "exit", "3"}
	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: []string{bin, "exit", "0"}, Cwd: ".", TimeoutSec: 30,
		Verify: verify, VerifyTimeoutSec: 20,
	})
	final, ok := hub.jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("hub job %s not found", created.ID)
	}
	if final.Status != job.StatusFailed {
		t.Fatalf("status = %s (err=%s), want failed (the worker's verify step failed)", final.Status, final.Error)
	}
	if final.Error == "" || !strings.Contains(final.Error, "verify failed") {
		t.Fatalf("error = %q, want the worker's verify failure", final.Error)
	}
	if final.Verify == nil {
		t.Fatal("the worker's verify result did not reach the hub job")
	}
	if final.Verify.Status != job.VerifyFailed || final.Verify.ExitCode != 3 {
		t.Fatalf("hub verify = %+v, want failed/exit 3", *final.Verify)
	}
	if len(final.Verify.Command) != len(verify) || final.Verify.Command[0] != verify[0] {
		t.Fatalf("hub verify command = %v, want the dispatched argv %v", final.Verify.Command, verify)
	}

	// The worker's stderr banners were mirrored to the hub job's stderr log.
	logs := getLogs(t, hub.ts, created.ID, "stderr")
	if !strings.Contains(logs, "===== gofer verify: "+strings.Join(verify, " ")+" =====") {
		t.Fatalf("hub stderr is missing the verify banner:\n%s", logs)
	}
	if got := strings.Count(logs, "===== gofer verify:"); got != 2 {
		t.Fatalf("hub stderr carries %d verify banner lines, want exactly the worker's 2 (open + close):\n%s", got, logs)
	}

	// And it is persisted, so a later `job show` reads it from the row.
	rec, ok, err := hub.store.GetJob(created.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob persisted: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(rec.VerifyJSON, `"status":"failed"`) || !strings.Contains(rec.VerifyJSON, `"exit_code":3`) {
		t.Fatalf("persisted verify_json = %q, want the worker's failed/exit 3 result", rec.VerifyJSON)
	}

	cancel()
	select {
	case <-clientErr:
	case <-time.After(5 * time.Second):
		t.Fatal("worker client did not stop")
	}
}

// TestDispatchRejectsWorkerBelowRequiredProtocol: a peer that predates a capability
// is REFUSED the job before anything is dispatched (G032: no silent degradation —
// the fields would be ignored and the job would run without its verify step /
// continuation / read-only mode). Each case names the missing capability so an
// operator knows exactly which worker to upgrade.
func TestDispatchRejectsWorkerBelowRequiredProtocol(t *testing.T) {
	bin := testcmd.Path(t)

	// v7 predates the verify dispatch fields and the job_event frame (both v8).
	t.Run("verify", func(t *testing.T) {
		hub := buildHubSide(t)
		dialFakeWorker(t, hub.ts.URL, 7)
		waitWorkerOnline(t, hub.hub)

		created := createJob(t, hub.ts, job.JobRequest{
			ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
			Cmd: []string{bin, "exit", "0"}, Cwd: ".", TimeoutSec: 30,
			Verify: []string{bin, "exit", "0"},
		})
		final, ok := hub.jobs.Wait(created.ID)
		if !ok {
			t.Fatalf("hub job %s not found", created.ID)
		}
		if final.Status != job.StatusFailed {
			t.Fatalf("status = %s, want failed", final.Status)
		}
		if !strings.Contains(final.Error, "protocol v7 lacks verify") || !strings.Contains(final.Error, "upgrade the worker") {
			t.Fatalf("error = %q, want it to name the missing capability and the upgrade", final.Error)
		}
	})

	// v5 predates the resume/read-only dispatch fields (v6).
	t.Run("session_and_read_only", func(t *testing.T) {
		hub := buildHubSide(t)
		dialFakeWorker(t, hub.ts.URL, 5)
		waitWorkerOnline(t, hub.hub)

		cases := []struct {
			name  string
			req   job.JobRequest
			lacks string
		}{
			{
				// ResumedFrom/SessionID are stamped exactly as ResumeJob stamps them
				// (the resume编排 has its own tests); what this pins is the
				// DISPATCH-time capability gate for a continuation.
				name: "session_id",
				req: job.JobRequest{
					ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
					Cmd: []string{bin, "exit", "0"}, Cwd: ".", TimeoutSec: 30,
					SessionID: "sess-v5", ResumedFrom: "src-v5",
				},
				lacks: "session_id",
			},
			{
				name: "read_only",
				req: job.JobRequest{
					ProjectKey: "alpha", Agent: "wrapper", Runner: "remote-w1", WorkerID: e2eWorkerID,
					Cmd: []string{bin, "exit", "0"}, Cwd: ".", TimeoutSec: 30, ReadOnly: true,
				},
				lacks: "read_only",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				res, err := hub.jobs.Submit(tc.req)
				if err != nil {
					t.Fatalf("Submit: %v", err)
				}
				final, ok := hub.jobs.Wait(res.ID)
				if !ok {
					t.Fatalf("hub job %s not found", res.ID)
				}
				if final.Status != job.StatusFailed {
					t.Fatalf("status = %s, want failed", final.Status)
				}
				if !strings.Contains(final.Error, "protocol v5 lacks "+tc.lacks) {
					t.Fatalf("error = %q, want %q", final.Error, "protocol v5 lacks "+tc.lacks)
				}
				if !strings.Contains(final.Error, "upgrade the worker") {
					t.Fatalf("error = %q, want an upgrade prompt", final.Error)
				}
			})
		}
	})
}

// dialFakeWorker dials the REAL hub as a worker that reports protocol `proto` and
// then only reads frames: it never executes anything. It lets a test pin what the
// hub does with a peer that predates a capability without shipping an old worker
// build (the negotiation is the hub's, and it is what decides refusal).
func dialFakeWorker(t *testing.T, hubURL string, proto int) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(hubURL, "http") + "/v1/workers/connect"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	header := http.Header{}
	header.Set("Authorization", "Bearer "+e2eToken)
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("dial hub as a fake v%d worker: %v", proto, err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })

	// Advertise the project + agents the hub's admission gate checks, so a refusal
	// can only come from the PROTOCOL capability check.
	reg := wsproto.Register{
		WorkerID:        e2eWorkerID,
		InstanceID:      "fake-v" + strconv.Itoa(proto),
		ProtocolVersion: proto,
		Projects:        []string{"alpha"},
		Agents:          []string{"exec", "wrapper"},
	}
	payload, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("marshal register: %v", err)
	}
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeRegister, Payload: payload}); err != nil {
		t.Fatalf("write register: %v", err)
	}
	var env wsproto.Envelope
	if err := wsjson.Read(ctx, conn, &env); err != nil {
		t.Fatalf("read registered ack: %v", err)
	}
	if env.Type != wsproto.TypeRegistered {
		t.Fatalf("first hub frame = %s, want %s", env.Type, wsproto.TypeRegistered)
	}
	ack, err := wsproto.As[wsproto.Registered](env)
	if err != nil {
		t.Fatalf("decode registered ack: %v", err)
	}
	if !ack.Accepted {
		t.Fatalf("hub rejected the fake v%d worker: %s", proto, ack.Reason)
	}
	return conn
}
