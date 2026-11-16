package wshub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// seqSink is a fakeSink that records the seq of every mirrored event and reports canned
// v30 resume cursors.
type seqSink struct {
	*fakeSink
	smu              sync.Mutex
	seqs             []int64
	evSeq, acpOffset int64
}

func (s *seqSink) OnJobEvent(ev wsproto.JobEvent) {
	s.smu.Lock()
	s.seqs = append(s.seqs, ev.Seq)
	s.smu.Unlock()
	s.fakeSink.OnJobEvent(ev)
}

func (s *seqSink) ResumeCursor() (int64, int64) { return s.evSeq, s.acpOffset }

func (s *seqSink) seqSnapshot() []int64 {
	s.smu.Lock()
	defer s.smu.Unlock()
	return append([]int64(nil), s.seqs...)
}

func pushEvents(t *testing.T, ctx context.Context, conn *websocket.Conn, sink *fakeSink, evs ...wsproto.JobEvent) {
	t.Helper()
	for _, ev := range evs {
		if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeJobEvent, JobID: ev.JobID, Payload: mustRaw(ev)}); err != nil {
			t.Fatalf("write job event: %v", err)
		}
	}
	// The single read loop is in order: once the result is observed every event frame
	// before it has been handled.
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeResult, JobID: "j1", Payload: mustRaw(wsproto.Result{JobID: "j1", Status: "done"})}); err != nil {
		t.Fatalf("write result: %v", err)
	}
	select {
	case <-sink.finished:
	case <-ctx.Done():
		t.Fatal("did not observe the terminal result")
	}
}

// TestHubNumberedJobEventsBypassConnectionDedup: against a v30 worker the hub hands
// EVERY numbered event to the sink (the durable per-job cursor, not the connection's
// identity window, decides) — two events with the same type/ts/interaction but different
// seq are both delivered.
func TestHubNumberedJobEventsBypassConnectionDedup(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, reg := dialAndRegister(t, ctx, wsURL, "w1") // registers at CurrentProtocolVersion (>= 30)
	defer conn.Close(websocket.StatusNormalClosure, "")
	if !reg.Accepted {
		t.Fatal("register rejected")
	}
	sink := &seqSink{fakeSink: newFakeSink()}
	if err := registerSink(t, hub, "w1", "j1", sink); err != nil {
		t.Fatal(err)
	}
	ev := wsproto.JobEvent{JobID: "j1", Type: "job.permission_requested", TS: 1700000000, InteractionID: "i-1"}
	e1, e2, replay := ev, ev, ev
	e1.Seq, e2.Seq, replay.Seq = 1, 2, 1
	pushEvents(t, ctx, conn, sink.fakeSink, e1, e2, replay)

	got := sink.seqSnapshot()
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 1 {
		t.Fatalf("sink saw seqs %v, want [1 2 1]: the sink (not the connection) dedups numbered events", got)
	}
}

// TestHubOldWorkerKeepsConnectionDedup: a worker below v30 is on the old path even if a
// frame carried a seq — the per-connection identity window still drops the duplicate.
func TestHubOldWorkerKeepsConnectionDedup(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _ := dialRegisterRaw(t, ctx, wsURL, wsproto.Register{WorkerID: "w1", ProtocolVersion: wsproto.JobEventSeqMinProtocolVersion - 1})
	defer conn.Close(websocket.StatusNormalClosure, "")
	sink := &seqSink{fakeSink: newFakeSink()}
	if err := registerSink(t, hub, "w1", "j1", sink); err != nil {
		t.Fatal(err)
	}
	ev := wsproto.JobEvent{JobID: "j1", Type: "job.permission_requested", TS: 1700000000, InteractionID: "i-1", Seq: 1}
	pushEvents(t, ctx, conn, sink.fakeSink, ev, ev)
	if got := sink.seqSnapshot(); len(got) != 1 {
		t.Fatalf("sink saw %v, want the duplicate dropped on the old path", got)
	}
}

func resumeAckFor(t *testing.T, proto int) wsproto.ResumeJob {
	t.Helper()
	hub := recoverHub(5 * time.Second)
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _ := dialRegisterRaw(t, ctx, wsURL, wsproto.Register{WorkerID: "w1", InstanceID: "inst-1", ProtocolVersion: wsproto.CurrentProtocolVersion})
	waitWorkerOnline(t, hub, "w1")
	sink := &seqSink{fakeSink: newFakeSink(), evSeq: 7, acpOffset: 123}
	sink.stdoutOff, sink.stderrOff = 11, 4
	if err := registerSink(t, hub, "w1", "j1", sink); err != nil {
		t.Fatal(err)
	}
	if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: "j1"}); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "blip")
	waitFor(t, func() bool { return len(sink.snapshot()) > 0 && sink.snapshot()[0] == "suspend:worker disconnected" })

	conn2, ack := dialRegisterRaw(t, ctx, wsURL, wsproto.Register{
		WorkerID: "w1", InstanceID: "inst-1", ProtocolVersion: proto,
		Inflight: []wsproto.InflightJob{{JobID: "j1", Status: "running"}},
	})
	defer conn2.Close(websocket.StatusNormalClosure, "")
	if len(ack.Resume) != 1 || ack.Resume[0].JobID != "j1" {
		t.Fatalf("ack.Resume = %+v, want [j1]", ack.Resume)
	}
	return ack.Resume[0]
}

// TestResumeAckCarriesEventCursor: a v30 worker's resume ack carries the job's applied
// event seq and mirrored acp size; an older worker's ack is exactly the old shape.
func TestResumeAckCarriesEventCursor(t *testing.T) {
	r := resumeAckFor(t, wsproto.JobEventSeqMinProtocolVersion)
	if r.EventSeq != 7 || r.AcpOff != 123 || r.StdoutOff != 11 || r.StderrOff != 4 {
		t.Fatalf("v30 resume entry = %+v, want event_seq 7 / acp_off 123 / offsets 11,4", r)
	}
	old := resumeAckFor(t, wsproto.JobEventSeqMinProtocolVersion-1)
	if old.EventSeq != 0 || old.AcpOff != 0 || old.StdoutOff != 11 {
		t.Fatalf("v29 resume entry = %+v, want no cursors", old)
	}
}

type fakeAdopter struct{ sink JobSink }

func (a fakeAdopter) AdoptAfterRestart(_, _ string, inflight []wsproto.InflightJob) (map[string]AdoptedJob, []string) {
	out := map[string]AdoptedJob{}
	for _, f := range inflight {
		out[f.JobID] = AdoptedJob{Sink: a.sink, StdoutOff: 3, StderrOff: 2, EventSeq: 5, AcpOff: 77}
	}
	return out, nil
}

// TestAdoptedJobResumeAckCarriesCursors: a job adopted after a serve restart gets the
// cursors the previous process persisted (applied event seq, mirrored acp size) in the
// ack of a v30 worker, so the replayed events and acp chunks continue from there.
func TestAdoptedJobResumeAckCarriesCursors(t *testing.T) {
	hub := recoverHub(5 * time.Second)
	hub.SetAdopter(fakeAdopter{sink: newFakeSink()})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, ack := dialRegisterRaw(t, ctx, wsURL, wsproto.Register{
		WorkerID: "w1", InstanceID: "inst-1", ProtocolVersion: wsproto.CurrentProtocolVersion,
		Inflight: []wsproto.InflightJob{{JobID: "adopted-1", Status: "running"}},
	})
	defer conn.Close(websocket.StatusNormalClosure, "")
	if len(ack.Resume) != 1 {
		t.Fatalf("ack.Resume = %+v, want the adopted job", ack.Resume)
	}
	r := ack.Resume[0]
	if r.EventSeq != 5 || r.AcpOff != 77 || r.StdoutOff != 3 || r.StderrOff != 2 {
		t.Fatalf("adopted resume entry = %+v, want event_seq 5 / acp_off 77 / offsets 3,2", r)
	}
}
