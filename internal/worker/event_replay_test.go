package worker

import (
	"encoding/json"
	"testing"
	"time"

	"context"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/wsproto"
)

// replayClient builds a client with one tracked hub job ("job-1" / local "local-1") that
// talks to a hub reporting protocol proto.
func replayClient(t *testing.T, proto int) *Client {
	t.Helper()
	cl := New(Config{WorkerID: "w1", Token: "t"}, newRunningJobs(t))
	cl.serverProto.Store(int64(proto))
	cl.inflightCreate("job-1")
	cl.putJobMapping("job-1", "local-1")
	return cl
}

func raiseEvents(cl *Client, n int) {
	for i := 0; i < n; i++ {
		cl.observeJobEvent("local-1", job.EventJobVerifyStarted, map[string]any{"i": i})
	}
}

// framesOf returns the (type, seq) of the frames the recording hub saw, in order.
func collectFrames(frames chan wsproto.Envelope, want int) []wsproto.Envelope {
	var out []wsproto.Envelope
	deadline := time.After(10 * time.Second)
	for len(out) < want {
		select {
		case env := <-frames:
			out = append(out, env)
		case <-deadline:
			return out
		}
	}
	return out
}

func eventSeqs(t *testing.T, envs []wsproto.Envelope) []int64 {
	t.Helper()
	var out []int64
	for _, env := range envs {
		if env.Type != wsproto.TypeJobEvent {
			continue
		}
		ev, err := wsproto.As[wsproto.JobEvent](env)
		if err != nil {
			t.Fatalf("decode job event: %v", err)
		}
		out = append(out, ev.Seq)
	}
	return out
}

// TestJobEventsBufferedAndReplayedAfterAck: events raised while the hub is away are
// numbered and kept; the resume ack drops what the hub already applied and the rest is
// replayed in order, exactly once.
func TestJobEventsBufferedAndReplayedAfterAck(t *testing.T) {
	cl := replayClient(t, wsproto.JobEventSeqMinProtocolVersion)
	raiseEvents(cl, 3) // no connection: nothing is written, everything is buffered

	frames := make(chan wsproto.Envelope, 16)
	conn := dialRecordConn(t, frames)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cl.applyResume(ctx, conn, []wsproto.ResumeJob{{JobID: "job-1", EventSeq: 1}})

	got := eventSeqs(t, collectFrames(frames, 2))
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("replayed seqs = %v, want [2 3] (seq 1 was acked)", got)
	}
	// The hub has now applied 3: a further reconnect replays nothing.
	cl.applyResume(ctx, conn, []wsproto.ResumeJob{{JobID: "job-1", EventSeq: 3}})
	time.Sleep(100 * time.Millisecond)
	if extra := eventSeqs(t, drainFrames(frames)); len(extra) != 0 {
		t.Fatalf("second reconnect replayed %v, want nothing after the ack", extra)
	}
	// The next event continues the numbering instead of restarting it.
	raiseEvents(cl, 1)
	ev := <-cl.jobEvents
	_ = ev
	cl.inflMu.Lock()
	last := cl.inflight["job-1"].evBuf[len(cl.inflight["job-1"].evBuf)-1].Seq
	cl.inflMu.Unlock()
	if last != 4 {
		t.Fatalf("next seq = %d, want 4", last)
	}
}

// TestJobEventBufferOverflowReportsLost: past the per-job cap the OLDEST unsent events
// are dropped and counted, and the replay ends with a job.events_lost notice carrying
// the count.
func TestJobEventBufferOverflowReportsLost(t *testing.T) {
	cl := replayClient(t, wsproto.JobEventSeqMinProtocolVersion)
	raiseEvents(cl, jobEventBufPerJob+44)

	cl.inflMu.Lock()
	n, lost := len(cl.inflight["job-1"].evBuf), cl.inflight["job-1"].evLost
	cl.inflMu.Unlock()
	if n != jobEventBufPerJob || lost != 44 {
		t.Fatalf("buffer = %d lost = %d, want %d / 44", n, lost, jobEventBufPerJob)
	}

	frames := make(chan wsproto.Envelope, jobEventBufPerJob+16)
	conn := dialRecordConn(t, frames)
	cl.applyResume(context.Background(), conn, []wsproto.ResumeJob{{JobID: "job-1"}})
	envs := collectFrames(frames, jobEventBufPerJob+1)
	var first, notice wsproto.JobEvent
	for i, env := range envs {
		ev, _ := wsproto.As[wsproto.JobEvent](env)
		if i == 0 {
			first = ev
		}
		if ev.Type == job.EventJobEventsLost {
			notice = ev
		}
	}
	if first.Seq != 45 {
		t.Fatalf("first replayed seq = %d, want 45 (the 44 oldest were dropped)", first.Seq)
	}
	if notice.Type != job.EventJobEventsLost {
		t.Fatalf("no job.events_lost notice in the replay (%d frames)", len(envs))
	}
	var d struct{ Count int64 }
	if err := json.Unmarshal(notice.Detail, &d); err != nil || d.Count != 44 {
		t.Fatalf("notice detail = %s (%v), want count 44", notice.Detail, err)
	}
}

// TestJobEventOverflowOfSentEventsIsNotLost: an event that already went out and is then
// pushed out of the buffer is not missing — only never-sent ones are reported.
func TestJobEventOverflowOfSentEventsIsNotLost(t *testing.T) {
	cl := replayClient(t, wsproto.JobEventSeqMinProtocolVersion)
	raiseEvents(cl, 10)
	cl.markJobEventSent("job-1", 10)
	raiseEvents(cl, jobEventBufPerJob)
	cl.inflMu.Lock()
	lost := cl.inflight["job-1"].evLost
	cl.inflMu.Unlock()
	if lost != 0 {
		t.Fatalf("lost = %d, want 0: the dropped events had been written", lost)
	}
}

// TestJobEventBufferGlobalCap: many chatty jobs together never hold more than the
// global cap.
func TestJobEventBufferGlobalCap(t *testing.T) {
	cl := replayClient(t, wsproto.JobEventSeqMinProtocolVersion)
	for i := 0; i < 20; i++ {
		rid, lid := "g-"+string(rune('a'+i)), "gl-"+string(rune('a'+i))
		cl.inflightCreate(rid)
		cl.putJobMapping(rid, lid)
		for j := 0; j < jobEventBufPerJob; j++ {
			cl.observeJobEvent(lid, job.EventJobVerifyStarted, nil)
		}
	}
	cl.inflMu.Lock()
	total := cl.evTotalLocked()
	cl.inflMu.Unlock()
	if total > jobEventBufGlobal {
		t.Fatalf("buffered %d events, want <= %d", total, jobEventBufGlobal)
	}
}

// TestJobEventsOldHubKeepsOldPath: against a hub below v30 events carry no seq, nothing
// is buffered, and a resume ack replays no events nor rewinds the acp offset.
func TestJobEventsOldHubKeepsOldPath(t *testing.T) {
	cl := replayClient(t, wsproto.JobEventSeqMinProtocolVersion-1)
	raiseEvents(cl, 3)
	ev := <-cl.jobEvents
	if ev.Seq != 0 {
		t.Fatalf("event seq = %d for a v29 hub, want 0", ev.Seq)
	}
	cl.inflMu.Lock()
	buffered := len(cl.inflight["job-1"].evBuf)
	cl.inflMu.Unlock()
	if buffered != 0 {
		t.Fatalf("buffered %d events for a v29 hub, want 0", buffered)
	}

	cl.inflightCommit("job-1", wsproto.LogStreamACP, 100, 1)
	frames := make(chan wsproto.Envelope, 8)
	conn := dialRecordConn(t, frames)
	cl.applyResume(context.Background(), conn, []wsproto.ResumeJob{{JobID: "job-1", AcpOff: 10}})
	if got := cl.inflightOffset("job-1", wsproto.LogStreamACP); got != 100 {
		t.Fatalf("acp offset = %d, want 100 (no rewind against an old hub)", got)
	}
	time.Sleep(100 * time.Millisecond)
	if got := drainFrames(frames); len(got) != 0 {
		t.Fatalf("frames sent against an old hub: %d", len(got))
	}
}

// TestResumeRewindsACPOffset: against a v30 hub the ack's AcpOff rewinds the acp mirror
// offset like stdout/stderr, so a chunk lost on a dropped connection is re-sent.
func TestResumeRewindsACPOffset(t *testing.T) {
	cl := replayClient(t, wsproto.JobEventSeqMinProtocolVersion)
	cl.inflightCommit("job-1", wsproto.LogStreamACP, 100, 1)
	frames := make(chan wsproto.Envelope, 8)
	conn := dialRecordConn(t, frames)
	cl.applyResume(context.Background(), conn, []wsproto.ResumeJob{{JobID: "job-1", AcpOff: 40}})
	if got := cl.inflightOffset("job-1", wsproto.LogStreamACP); got != 40 {
		t.Fatalf("acp offset = %d, want 40", got)
	}
}

// TestTerminalWhileDisconnectedReplaysEventsOutcomeResult: a job that ends with the hub
// away reaches it as unconfirmed events, then the Outcome, then the Result.
func TestTerminalWhileDisconnectedReplaysEventsOutcomeResult(t *testing.T) {
	cl := replayClient(t, wsproto.JobEventSeqMinProtocolVersion)
	raiseEvents(cl, 2)
	outcome := wsproto.Outcome{JobID: "job-1", RenderedCommand: "echo hi"}
	res := wsproto.Result{JobID: "job-1", Status: job.StatusDone}
	if err := cl.sendTerminal(context.Background(), "job-1", &outcome, res); err == nil {
		t.Fatal("sendTerminal with no connection must report the failure")
	}
	if cached, ok := cl.inflightResult("job-1"); !ok || cached.Status != job.StatusDone {
		t.Fatalf("Result not cached: %+v %v", cached, ok)
	}

	frames := make(chan wsproto.Envelope, 16)
	conn := dialRecordConn(t, frames)
	// The job is terminal, so the hub's ack has no resume entry for it.
	cl.applyResume(context.Background(), conn, nil)
	envs := collectFrames(frames, 4)
	var types []wsproto.FrameType
	for _, env := range envs {
		types = append(types, env.Type)
	}
	want := []wsproto.FrameType{wsproto.TypeJobEvent, wsproto.TypeJobEvent, wsproto.TypeOutcome, wsproto.TypeResult}
	if len(types) != len(want) {
		t.Fatalf("frames = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("frames = %v, want %v", types, want)
		}
	}
	if ids := cl.inflightIDs(); len(ids) != 0 {
		t.Fatalf("in-flight = %v, want the entry dropped once the Result landed", ids)
	}
}
