package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/wsproto"
)

// This file covers RECOV-01 R4 end to end: the host row must record WHICH worker
// PROCESS owns a dispatched job, and a hub that starts over a jobstore holding
// `recovering` rows must ADOPT the jobs of the worker process that reconnects — logs
// included, offsets and all.

// rawFrame marshals a payload into an envelope's raw body for a hand-written frame.
func rawFrame(t *testing.T, payload any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal frame payload: %v", err)
	}
	return b
}

// dialRawWorker dials the hub's worker endpoint with w1's token and REGISTERS with an
// explicit frame. A raw client is required for the adoption tests: the real worker
// client reports what it actually holds, while these tests must state a specific
// instance id / inflight list to drive the hub's adoption decision.
func dialRawWorker(t *testing.T, hub *hubSide, ctx context.Context, reg wsproto.Register) (*websocket.Conn, wsproto.Registered) {
	t.Helper()
	wsURL := "ws" + hub.ts.URL[len("http"):] + "/v1/workers/connect"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + e2eToken}},
	})
	if err != nil {
		t.Fatalf("dial hub: %v", err)
	}
	conn.SetReadLimit(1 << 20)
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeRegister, Payload: rawFrame(t, reg)}); err != nil {
		t.Fatalf("write register: %v", err)
	}
	var env wsproto.Envelope
	if err := wsjson.Read(ctx, conn, &env); err != nil {
		t.Fatalf("read registered: %v", err)
	}
	if env.Type != wsproto.TypeRegistered {
		t.Fatalf("first frame = %q, want registered", env.Type)
	}
	ack, err := wsproto.As[wsproto.Registered](env)
	if err != nil {
		t.Fatalf("decode registered: %v", err)
	}
	if !ack.Accepted {
		t.Fatalf("register rejected: %s", ack.Reason)
	}
	return conn, ack
}

// rawWrite sends one hand-written frame from the raw worker connection.
func rawWrite(t *testing.T, ctx context.Context, conn *websocket.Conn, frameType wsproto.FrameType, jobID string, payload any) {
	t.Helper()
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: frameType, JobID: jobID, Payload: rawFrame(t, payload)}); err != nil {
		t.Fatalf("write %s frame: %v", frameType, err)
	}
}

// rawReadType reads frames until one of the wanted type arrives (skipping heartbeats),
// or fails the test on timeout.
func rawReadType(t *testing.T, ctx context.Context, conn *websocket.Conn, want wsproto.FrameType, timeout time.Duration) wsproto.Envelope {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		rctx, cancel := context.WithDeadline(ctx, deadline)
		var env wsproto.Envelope
		err := wsjson.Read(rctx, conn, &env)
		cancel()
		if err != nil {
			t.Fatalf("waiting for a %s frame: %v", want, err)
		}
		if env.Type == want {
			return env
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s frame within %s", want, timeout)
		}
	}
}

// seedRecoveringJob writes a job row exactly as ReconcileOrphanJobs leaves it after a
// serve restart: held in `recovering`, dispatched by the given worker PROCESS instance,
// with whatever the previous serve had already mirrored into stdout.log on disk.
func seedRecoveringJob(t *testing.T, hub *hubSide, jobID, instanceID, stdout string) string {
	t.Helper()
	dir := filepath.Join(hub.root, "alpha", jobID[:8], jobID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir result dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stdout.log"), []byte(stdout), 0o644); err != nil {
		t.Fatalf("seed stdout.log: %v", err)
	}
	now := time.Now().Unix()
	rec := jobstore.JobRecord{
		ID: jobID, ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1",
		Status: job.StatusRecovering, WorkerID: e2eWorkerID, WorkerInstanceID: instanceID,
		// A throwaway cwd, NOT ".": the terminal outcome capture runs `git diff` in
		// the job's cwd, and "." is this package inside the gofer checkout — on a
		// slow mount that alone blows the 5s status waits below.
		Cwd: t.TempDir(), ResultDir: dir, StartedAt: now - 30, UpdatedAt: now, RecoveringSince: now - 5,
		Error: "recovering: orphaned: serve restarted while job was non-terminal",
	}
	if err := hub.store.UpsertJob(rec); err != nil {
		t.Fatalf("upsert recovering row: %v", err)
	}
	return dir
}

// TestAdoptOrphanRecoveringJobOnRegister: the serve-restart case. A fresh hub (no
// in-memory sink for anything) receives a Register from the SAME worker process that
// owned a store-held `recovering` job. The job must be adopted back to `running`, the
// ack must tell the worker to rewind to the offsets already on disk (no gap, no
// duplicate), the log frames must land in the SAME result-dir files, and the worker's
// Result must finish it through the normal classify/finish path (outcome included).
func TestAdoptOrphanRecoveringJobOnRegister(t *testing.T) {
	hub := buildHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const (
		jobID      = "20260916-120000-adopt01"
		instanceID = "inst-adopt"
		before     = "BEFORE-RESTART\n"
		after      = "AFTER-RESTART\n"
	)
	dir := seedRecoveringJob(t, hub, jobID, instanceID, before)

	conn, ack := dialRawWorker(t, hub, ctx, wsproto.Register{
		WorkerID: e2eWorkerID, InstanceID: instanceID, ProtocolVersion: wsproto.CurrentProtocolVersion,
		Inflight: []wsproto.InflightJob{{JobID: jobID, Status: "running", StdoutOff: int64(len(before)), Seq: 4}},
	})
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	if len(ack.Resume) != 1 || ack.Resume[0].JobID != jobID {
		t.Fatalf("ack.Resume = %+v, want exactly [%s]", ack.Resume, jobID)
	}
	if ack.Resume[0].StdoutOff != int64(len(before)) {
		t.Fatalf("resume stdout offset = %d, want %d (what is already persisted on disk)",
			ack.Resume[0].StdoutOff, len(before))
	}
	waitHostStatus(t, hub.jobs, jobID, job.StatusRunning, 5*time.Second)

	rawWrite(t, ctx, conn, wsproto.TypeLog, jobID, wsproto.Log{JobID: jobID, Stream: "stdout", Seq: 5, Text: after})
	rawWrite(t, ctx, conn, wsproto.TypeOutcome, jobID, wsproto.Outcome{
		JobID: jobID, RenderedCommand: `{"command":"echo","args":["hi"]}`,
	})
	rawWrite(t, ctx, conn, wsproto.TypeResult, jobID, wsproto.Result{JobID: jobID, Status: "done", ExitCode: 0})

	waitHostStatus(t, hub.jobs, jobID, job.StatusDone, 10*time.Second)
	final, _ := hub.jobs.Get(jobID)
	if final.WorkerID != e2eWorkerID || final.WorkerInstanceID != instanceID {
		t.Fatalf("adopted row lost its worker identity: (%q, %q)", final.WorkerID, final.WorkerInstanceID)
	}
	if final.Source != "worker:"+e2eWorkerID {
		t.Fatalf("job source = %q, want the worker that ran it", final.Source)
	}
	if final.RenderedCommand != `{"command":"echo","args":["hi"]}` {
		t.Fatalf("rendered_command = %q, want the worker-captured outcome applied", final.RenderedCommand)
	}
	if final.RecoveringSince != 0 {
		t.Fatalf("recovering_since = %d, want cleared once the job was adopted", final.RecoveringSince)
	}

	got, err := os.ReadFile(filepath.Join(dir, "stdout.log"))
	if err != nil {
		t.Fatalf("read adopted stdout.log: %v", err)
	}
	if string(got) != before+after {
		t.Fatalf("stdout.log = %q, want %q (the pre-restart output must be continued, not replaced)", got, before+after)
	}
}

// TestAdoptRejectsInstanceMismatch: after a restart only the SAME worker process may
// take a store-held `recovering` job back. A job recorded for another instance, and a
// job the reconnecting worker does not report in flight, must both end at once with
// "worker lost: job not tracked after restart" — no window can ever adopt them.
func TestAdoptRejectsInstanceMismatch(t *testing.T) {
	hub := buildHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const (
		mismatchJob = "20260916-120000-adopt02"
		absentJob   = "20260916-120000-adopt03"
		instanceID  = "inst-new"
	)
	seedRecoveringJob(t, hub, mismatchJob, "inst-old", "old-process-output\n")
	seedRecoveringJob(t, hub, absentJob, instanceID, "same-process-output\n")

	conn, ack := dialRawWorker(t, hub, ctx, wsproto.Register{
		WorkerID: e2eWorkerID, InstanceID: instanceID, ProtocolVersion: wsproto.CurrentProtocolVersion,
		// Reports tracking NOTHING: the mismatch job belongs to another process and the
		// second job is simply not claimed.
		Inflight: []wsproto.InflightJob{},
	})
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	if len(ack.Resume) != 0 {
		t.Fatalf("ack.Resume = %+v, want none: neither job may be adopted", ack.Resume)
	}
	for _, id := range []string{mismatchJob, absentJob} {
		final, ok := hub.jobs.Get(id)
		if !ok {
			t.Fatalf("job %s vanished", id)
		}
		if final.Status != job.StatusFailed {
			t.Fatalf("job %s status = %s, want failed", id, final.Status)
		}
		if want := "worker lost: job not tracked after restart"; final.Error != want {
			t.Fatalf("job %s error = %q, want %q", id, final.Error, want)
		}
	}
}

// TestAdoptedJobCancelReachesWorker: `job cancel` on an ADOPTED job must reach the
// worker process (there is no runner goroutine left to relay it) and the host job must
// end cancelled.
func TestAdoptedJobCancelReachesWorker(t *testing.T) {
	hub := buildHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const (
		jobID      = "20260916-120000-adopt04"
		instanceID = "inst-cancel"
	)
	seedRecoveringJob(t, hub, jobID, instanceID, "CANCELLED-SOON\n")

	conn, _ := dialRawWorker(t, hub, ctx, wsproto.Register{
		WorkerID: e2eWorkerID, InstanceID: instanceID, ProtocolVersion: wsproto.CurrentProtocolVersion,
		Inflight: []wsproto.InflightJob{{JobID: jobID, Status: "running"}},
	})
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	waitHostStatus(t, hub.jobs, jobID, job.StatusRunning, 5*time.Second)

	if err := hub.jobs.Cancel(jobID); err != nil {
		t.Fatalf("cancel adopted job: %v", err)
	}

	env := rawReadType(t, ctx, conn, wsproto.TypeCancel, 5*time.Second)
	if env.JobID != jobID {
		t.Fatalf("cancel frame job_id = %q, want %q", env.JobID, jobID)
	}
	cf, err := wsproto.As[wsproto.Cancel](env)
	if err != nil || cf.JobID != jobID {
		t.Fatalf("cancel frame payload = %+v (err=%v), want job %s", cf, err, jobID)
	}
	waitHostStatus(t, hub.jobs, jobID, job.StatusCancelled, 5*time.Second)
}

// TestE2EJobSurvivesServeRestart is the RECOV-01 R4 full-stack acceptance. Unlike the
// blip test (one hub, one process), this one replaces the SERVE side: the worker keeps
// running, the hub that dispatched the job goes away, a NEW hub starts over the SAME
// jobstore and result dirs, and the worker reconnects to it. The job must be adopted
// back to `running`, finish `done`, and leave a stdout log that is the command's output
// EXACTLY once — the pre-restart part from disk, the rest replayed from the offsets the
// previous serve had persisted.
//
// The dying process is simulated by cutting its connections and closing its listener
// (in-process, so its Go state cannot be reaped); the recovery window is enabled on
// both sides so the old process holds the job instead of failing it on the cut.
func TestE2EJobSurvivesServeRestart(t *testing.T) {
	host := t.TempDir()
	root := t.TempDir()

	serveA := buildHubSideAt(t, host, root)
	serveA.hub.SetRecoverWindow(30 * time.Second)
	serveB := buildHubSideAt(t, host, root)
	serveB.hub.SetRecoverWindow(30 * time.Second)

	// The new serve's startup reconciliation holds the previous process's worker jobs in
	// `recovering` for the window (the row may still say `running` — the process died
	// before it could say anything).
	if n, err := serveB.jobs.ReconcileOrphanJobs(); err != nil {
		t.Fatalf("reconcile orphan jobs: %v", err)
	} else {
		t.Logf("new serve held %d orphan job(s)", n)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Both hubs are offered; the client dials them in order, so the job is dispatched on
	// A and the reconnect after A is gone lands on B.
	cl, _ := buildWorkerSideURLs(t, []string{serveA.ts.URL, serveB.ts.URL}, workerSideOpts{
		InitialBackoff: 200 * time.Millisecond,
		MaxBackoff:     400 * time.Millisecond,
	})
	clientErr := make(chan error, 1)
	go func() { clientErr <- cl.Run(ctx) }()
	waitWorkerOnline(t, serveA.hub)

	const lines = 12
	created := createJob(t, serveA.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: testcmd.Cmd(t, "stdout-lines", "LINE", "12", "400ms"), Cwd: ".", TimeoutSec: 120,
	})
	// Let the mirror genuinely stream before the restart.
	waitForLogContains(t, serveA.ts, created.ID, "LINE2", 20*time.Second)

	// --- the serve process goes away (the worker process is untouched) ---
	serveA.ts.CloseClientConnections()
	serveA.ts.Close()

	// The worker reconnects to the NEW hub; that register carries its inflight list, and
	// the new hub adopts the held row back to `running`.
	waitHostStatus(t, serveB.jobs, created.ID, job.StatusRunning, 30*time.Second)

	waitHostStatus(t, serveB.jobs, created.ID, job.StatusDone, 30*time.Second)
	final, _ := serveB.jobs.Get(created.ID)
	if final.ExitCode != 0 {
		t.Fatalf("exit_code = %d (err=%s), want 0", final.ExitCode, final.Error)
	}
	if final.WorkerInstanceID == "" {
		t.Fatal("adopted row has no worker_instance_id: the adoption could not have been decided")
	}

	var want string
	for i := 1; i <= lines; i++ {
		want += fmt.Sprintf("LINE%d\n", i)
	}
	if got := getLogs(t, serveB.ts, created.ID, "stdout"); got != want {
		t.Fatalf("host stdout log across the restart =\n%q\nwant exactly\n%q", got, want)
	}

	cancel()
	select {
	case <-clientErr:
	case <-time.After(3 * time.Second):
		t.Log("worker client did not exit promptly after cancel (non-fatal)")
	}
}
