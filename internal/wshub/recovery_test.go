package wshub

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// recoverHub builds a hub with RECOV-01 recovery enabled for the given window and a
// long read deadline (the tests drive disconnects by closing the connection, never by
// waiting out a heartbeat).
func recoverHub(window time.Duration) *Hub {
	h := shortHeartbeat(map[string]string{"w1": "w1"}, time.Second, 10*time.Second)
	h.SetRecoverWindow(window)
	return h
}

// dialRegisterRaw dials + registers with an EXPLICIT register frame, so a test can
// control Inflight (nil = a pre-RECOV-01 worker, empty = a new worker tracking
// nothing) — the distinction the hub keys its recovery decision on.
func dialRegisterRaw(t *testing.T, ctx context.Context, wsURL string, reg wsproto.Register) (*websocket.Conn, wsproto.Registered) {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.SetReadLimit(1 << 20)
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type:    wsproto.TypeRegister,
		Payload: mustRaw(reg),
	}); err != nil {
		t.Fatalf("write register: %v", err)
	}
	env, err := readEnvelope(ctx, conn)
	if err != nil {
		t.Fatalf("read registered: %v", err)
	}
	ack, _ := wsproto.As[wsproto.Registered](env)
	if !ack.Accepted {
		t.Fatalf("register rejected: %s", ack.Reason)
	}
	return conn, ack
}

// dispatchAndDrop registers a sink+dispatch for jobID on worker w1, closes the
// connection mid-job and returns the sink once the hub has reported it suspended.
func dispatchAndDrop(t *testing.T, ctx context.Context, hub *Hub, wsURL, instanceID, jobID string, stdoutOff, stderrOff int64) *fakeSink {
	t.Helper()
	conn, _ := dialRegisterRaw(t, ctx, wsURL, wsproto.Register{
		WorkerID: "w1", InstanceID: instanceID, ProtocolVersion: wsproto.CurrentProtocolVersion,
	})
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })

	sink := newFakeSink()
	sink.stdoutOff, sink.stderrOff = stdoutOff, stderrOff
	if err := hub.RegisterSink("w1", jobID, sink); err != nil {
		t.Fatalf("RegisterSink: %v", err)
	}
	if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: jobID}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "blip")
	waitFor(t, func() bool { return len(sink.snapshot()) > 0 && sink.snapshot()[0] == "suspend:worker disconnected" })
	return sink
}

// TestWorkerDisconnectEntersRecovering: with a recovery window set, a mid-job worker
// disconnect SUSPENDS the job (host job → recovering, offsets recorded) instead of
// failing it, and the job stays held for the window.
func TestWorkerDisconnectEntersRecovering(t *testing.T) {
	hub := recoverHub(2 * time.Second)
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sink := dispatchAndDrop(t, ctx, hub, wsURL, "inst-1", "j1", 11, 4)

	select {
	case err := <-sink.lost:
		t.Fatalf("job failed on disconnect instead of recovering: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	// The job must be held, with the server-side offsets recorded for the resume ack.
	hub.recMu.Lock()
	rs := hub.recov["w1"]
	var got *recoveringJob
	if rs != nil {
		got = rs.jobs["j1"]
	}
	hub.recMu.Unlock()
	if got == nil {
		t.Fatal("job j1 is not held in the worker's recovery set")
	}
	if got.stdoutOff != 11 || got.stderrOff != 4 {
		t.Fatalf("recorded offsets = (%d,%d), want (11,4)", got.stdoutOff, got.stderrOff)
	}
}

// TestSameInstanceReconnectResumesJob: the same worker PROCESS reconnecting within
// the window with the job still in its `inflight` list resumes it — the ack carries
// the server-side offsets the worker must rewind to, the sink is re-attached to the
// new connection (later frames reach it) and the job is never failed.
func TestSameInstanceReconnectResumesJob(t *testing.T) {
	hub := recoverHub(5 * time.Second)
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sink := dispatchAndDrop(t, ctx, hub, wsURL, "inst-1", "j1", 11, 4)

	conn, ack := dialRegisterRaw(t, ctx, wsURL, wsproto.Register{
		WorkerID: "w1", InstanceID: "inst-1", ProtocolVersion: wsproto.CurrentProtocolVersion,
		Inflight: []wsproto.InflightJob{{JobID: "j1", Status: "running", StdoutOff: 7, StderrOff: 4, Seq: 3}},
	})
	defer conn.Close(websocket.StatusNormalClosure, "done")

	if len(ack.Resume) != 1 || ack.Resume[0].JobID != "j1" {
		t.Fatalf("ack.Resume = %+v, want exactly [j1]", ack.Resume)
	}
	if ack.Resume[0].StdoutOff != 11 || ack.Resume[0].StderrOff != 4 {
		t.Fatalf("resume offsets = (%d,%d), want the SERVER-side (11,4)", ack.Resume[0].StdoutOff, ack.Resume[0].StderrOff)
	}
	select {
	case <-sink.resumed:
	case <-time.After(2 * time.Second):
		t.Fatal("job not resumed on a same-instance reconnect")
	}
	// The sink must now be attached to the NEW connection: a log frame there reaches it.
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type: wsproto.TypeLog, JobID: "j1",
		Payload: mustRaw(wsproto.Log{JobID: "j1", Stream: "stdout", Seq: 4, Text: "after-blip"}),
	}); err != nil {
		t.Fatalf("write log: %v", err)
	}
	waitFor(t, func() bool {
		ev := sink.snapshot()
		return len(ev) > 0 && ev[len(ev)-1] == "log:after-blip"
	})
	select {
	case err := <-sink.lost:
		t.Fatalf("resumed job was failed: %v", err)
	default:
	}
}

// TestRecoverWindowExpiryFailsWorkerLost: if the worker never comes back within the
// window, the held job is failed with errWorkerLost (error_code=worker_lost).
func TestRecoverWindowExpiryFailsWorkerLost(t *testing.T) {
	hub := recoverHub(150 * time.Millisecond)
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sink := dispatchAndDrop(t, ctx, hub, wsURL, "inst-1", "j1", 0, 0)

	select {
	case err := <-sink.lost:
		if err != errWorkerLost {
			t.Fatalf("OnDisconnect err = %v, want errWorkerLost", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("recovery window expiry did not fail the job")
	}
}

// TestNewInstanceFailsRecoveringImmediately: a reconnecting worker with a DIFFERENT
// instance_id is a restarted process — the old process's jobs died with it, so the
// recovering jobs are failed at once instead of waiting out the window.
func TestNewInstanceFailsRecoveringImmediately(t *testing.T) {
	hub := recoverHub(30 * time.Second) // long: only an immediate fail can satisfy this
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sink := dispatchAndDrop(t, ctx, hub, wsURL, "inst-1", "j1", 0, 0)

	conn, ack := dialRegisterRaw(t, ctx, wsURL, wsproto.Register{
		WorkerID: "w1", InstanceID: "inst-2", ProtocolVersion: wsproto.CurrentProtocolVersion,
		Inflight: []wsproto.InflightJob{{JobID: "j1", Status: "running"}},
	})
	defer conn.Close(websocket.StatusNormalClosure, "done")

	if len(ack.Resume) != 0 {
		t.Fatalf("ack.Resume = %+v, want none for a restarted worker", ack.Resume)
	}
	select {
	case err := <-sink.lost:
		if err != errWorkerLost {
			t.Fatalf("OnDisconnect err = %v, want errWorkerLost", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new instance did not fail the recovering job immediately")
	}
}

// TestLegacyWorkerFrameResumesWithinWindow covers the pre-RECOV-01-worker path: a
// worker that does not report `inflight` cannot confirm anything, so its jobs stay
// recovering under the window and the FIRST frame for one of them proves the worker
// (resume, no failure) — the window is not waited out.
func TestLegacyWorkerFrameResumesWithinWindow(t *testing.T) {
	hub := recoverHub(5 * time.Second)
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sink := dispatchAndDrop(t, ctx, hub, wsURL, "inst-1", "j1", 0, 0)

	// A legacy worker: the register frame carries no `inflight` key at all (nil).
	conn, ack := dialRegisterRaw(t, ctx, wsURL, wsproto.Register{
		WorkerID: "w1", InstanceID: "inst-1", ProtocolVersion: wsproto.CurrentProtocolVersion,
	})
	defer conn.Close(websocket.StatusNormalClosure, "done")
	if len(ack.Resume) != 0 {
		t.Fatalf("ack.Resume = %+v, want none for a worker that cannot confirm", ack.Resume)
	}
	select {
	case <-sink.resumed:
		t.Fatal("legacy worker job resumed without any proof the worker still has it")
	case <-time.After(150 * time.Millisecond):
	}

	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type: wsproto.TypeLog, JobID: "j1",
		Payload: mustRaw(wsproto.Log{JobID: "j1", Stream: "stdout", Seq: 1, Text: "still-here"}),
	}); err != nil {
		t.Fatalf("write log: %v", err)
	}
	select {
	case <-sink.resumed:
	case <-time.After(2 * time.Second):
		t.Fatal("a frame within the window did not resume the job")
	}
	waitFor(t, func() bool {
		ev := sink.snapshot()
		return len(ev) > 0 && ev[len(ev)-1] == "log:still-here"
	})
	select {
	case err := <-sink.lost:
		t.Fatalf("job was failed after being proven live: %v", err)
	default:
	}
}

// TestCancelDuringRecoveringDelivered: a cancel issued while the worker is offline
// is recorded and delivered as soon as the job is resumed, so the worker stops
// running a job the host has already finished as cancelled.
func TestCancelDuringRecoveringDelivered(t *testing.T) {
	hub := recoverHub(5 * time.Second)
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sink := dispatchAndDrop(t, ctx, hub, wsURL, "inst-1", "j1", 0, 0)

	// The host cancels while the worker is down.
	if err := hub.Cancel("w1", "j1"); err != ErrWorkerOffline {
		t.Fatalf("Cancel while offline = %v, want ErrWorkerOffline", err)
	}

	conn, ack := dialRegisterRaw(t, ctx, wsURL, wsproto.Register{
		WorkerID: "w1", InstanceID: "inst-1", ProtocolVersion: wsproto.CurrentProtocolVersion,
		Inflight: []wsproto.InflightJob{{JobID: "j1", Status: "running"}},
	})
	defer conn.Close(websocket.StatusNormalClosure, "done")
	if len(ack.Resume) != 1 || ack.Resume[0].JobID != "j1" {
		t.Fatalf("ack.Resume = %+v, want [j1]", ack.Resume)
	}

	// The deferred cancel must arrive on the new connection (frame order vs. the
	// resume is irrelevant; the hub may interleave a heartbeat ping).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rctx, rcancel := context.WithTimeout(ctx, 2*time.Second)
		env, err := readEnvelope(rctx, conn)
		rcancel()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if env.Type != wsproto.TypeCancel {
			continue
		}
		cf, _ := wsproto.As[wsproto.Cancel](env)
		if cf.JobID != "j1" {
			t.Fatalf("cancel job_id = %q, want j1", cf.JobID)
		}
		select {
		case <-sink.resumed:
		default:
			t.Fatal("cancel arrived before the job was resumed")
		}
		return
	}
	t.Fatal("cancel recorded during the outage was never delivered after the reconnect")
}
