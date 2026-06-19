package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/wshub"
	"github.com/inhere/gofer/internal/wsproto"
)

// fakeHub records the runner's calls and lets a test drive the sink (feed log
// frames / a result) to exercise the Run wait. It implements the dispatcher
// interface.
type fakeHub struct {
	mu            sync.Mutex
	calls         []string // "register" / "dispatch" / "deregister" in order
	sink          wshub.JobSink
	registerErr   error
	dispatchErr   error
	dispatchedCmd []string

	cancelCalls int // count of Cancel(workerID, jobID) calls
}

func (h *fakeHub) RegisterSink(_, _ string, sk wshub.JobSink) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, "register")
	if h.registerErr != nil {
		return h.registerErr
	}
	h.sink = sk
	return nil
}

func (h *fakeHub) DeregisterSink(_, _ string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, "deregister")
}

func (h *fakeHub) Dispatch(_ string, d wsproto.Dispatch) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, "dispatch")
	h.dispatchedCmd = d.Cmd
	return h.dispatchErr
}

func (h *fakeHub) Answer(_, _, _, _ string) error { return nil }

func (h *fakeHub) Cancel(_, _ string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancelCalls++
	return nil
}

func (h *fakeHub) snapshotCalls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

func (h *fakeHub) cancelCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cancelCalls
}

func (h *fakeHub) getSink() wshub.JobSink {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sink
}

// newRunnerWithHub builds a Runner wired to a fake hub (bypasses New's concrete
// *wshub.Hub requirement, exercising the same code path via the dispatcher
// interface).
func newRunnerWithHub(h dispatcher) *Runner {
	return &Runner{name: "remote-w1", workerID: "w1", hub: h}
}

func TestRunNilForward(t *testing.T) {
	r := newRunnerWithHub(&fakeHub{})
	res := r.Run(context.Background(), runner.Request{JobID: "j1"})
	if res.ExitCode != -1 || res.Err == nil {
		t.Fatalf("nil Forward should yield ExitCode -1 + err, got %+v", res)
	}
}

func TestRunWorkerOffline(t *testing.T) {
	r := newRunnerWithHub(&fakeHub{registerErr: wshub.ErrWorkerOffline})
	res := r.Run(context.Background(), runner.Request{JobID: "j1", Forward: &runner.Forward{}})
	if res.ExitCode != -1 || !errors.Is(res.Err, wshub.ErrWorkerOffline) {
		t.Fatalf("offline should surface ErrWorkerOffline, got %+v", res)
	}
}

// TestRunSinkLifecycle (review #2): RegisterSink must precede Dispatch, and
// DeregisterSink must run on exit.
func TestRunSinkLifecycle(t *testing.T) {
	h := &fakeHub{}
	r := newRunnerWithHub(h)
	var stdout, stderr bytes.Buffer

	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(context.Background(), runner.Request{
			JobID:  "j1",
			Stdout: &stdout,
			Stderr: &stderr,
			Forward: &runner.Forward{
				ProjectKey: "p", Agent: "exec", Cmd: []string{"echo", "hi"},
			},
		})
	}()

	// Wait for the sink to be registered, then feed a log + result.
	var sink wshub.JobSink
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sink = h.getSink(); sink != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if sink == nil {
		t.Fatal("sink never registered")
	}
	sink.WriteLog("stdout", 1, "hello")
	sink.Finish(wsproto.Result{JobID: "j1", Status: "done", ExitCode: 0})

	select {
	case res := <-done:
		if res.ExitCode != 0 || res.Err != nil {
			t.Fatalf("expected done result, got %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Finish")
	}

	calls := h.snapshotCalls()
	// Must be register → dispatch → ... → deregister.
	if len(calls) < 3 || calls[0] != "register" || calls[1] != "dispatch" || calls[len(calls)-1] != "deregister" {
		t.Fatalf("call order = %v, want register,dispatch,...,deregister", calls)
	}
	if stdout.String() != "hello" {
		t.Fatalf("stdout mirror = %q, want hello", stdout.String())
	}
}

func TestRunResultMapping(t *testing.T) {
	cases := []struct {
		status   string
		exitCode int
		wantErr  bool
	}{
		{"done", 0, false},
		{"failed", 3, true},
		{"timeout", -1, true},
		{"cancelled", -1, true},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			h := &fakeHub{}
			r := newRunnerWithHub(h)
			done := make(chan runner.Result, 1)
			go func() {
				done <- r.Run(context.Background(), runner.Request{JobID: "j1", Forward: &runner.Forward{}})
			}()
			// Wait for registration then finish.
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && h.getSink() == nil {
				time.Sleep(5 * time.Millisecond)
			}
			h.getSink().Finish(wsproto.Result{JobID: "j1", Status: tc.status, ExitCode: tc.exitCode})
			res := <-done
			if res.ExitCode != tc.exitCode {
				t.Fatalf("exit = %d, want %d", res.ExitCode, tc.exitCode)
			}
			if (res.Err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", res.Err, tc.wantErr)
			}
		})
	}
}

func TestRunCtxCancel(t *testing.T) {
	h := &fakeHub{}
	r := newRunnerWithHub(h)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(ctx, runner.Request{JobID: "j1", Forward: &runner.Forward{}})
	}()
	// Cancel without ever delivering a result.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.getSink() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case res := <-done:
		if res.Err == nil {
			t.Fatal("ctx cancel should surface an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return on ctx cancel")
	}
}

// TestBoundedSinkTruncates proves an oversize frame is capped and marked once.
func TestBoundedSinkTruncates(t *testing.T) {
	old := maxWSFrameBytes
	maxWSFrameBytes = 16
	defer func() { maxWSFrameBytes = old }()

	var out bytes.Buffer
	s := newBoundedSink(&out, &out)
	s.WriteLog("stdout", 1, strings.Repeat("a", 100))
	s.WriteLog("stdout", 2, strings.Repeat("b", 100)) // second oversize: no second marker

	got := out.String()
	if strings.Count(got, sinkTruncateMark) != 1 {
		t.Fatalf("expected exactly one truncation marker, got %d in %q", strings.Count(got, sinkTruncateMark), got)
	}
	// Strip the marker before counting capped payload bytes (the marker text itself
	// contains an 'a' in "truncated").
	payload := strings.ReplaceAll(got, sinkTruncateMark, "")
	if strings.Count(payload, "a") != 16 || strings.Count(payload, "b") != 16 {
		t.Fatalf("each oversize frame should be capped to 16 bytes: %q", got)
	}
}

// fakeSink is a runner.InteractionSink that records each Open call and hands back
// a controllable answer channel so the bridge's WaitAnswer goroutine can be
// driven from a test.
type fakeSink struct {
	mu      sync.Mutex
	opened  []runner.RemoteInteraction
	chans   map[string]chan string // interaction id → answer channel
	openErr error
}

func newFakeSink() *fakeSink { return &fakeSink{chans: map[string]chan string{}} }

func (f *fakeSink) Open(_ context.Context, it runner.RemoteInteraction) (<-chan string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return nil, f.openErr
	}
	f.opened = append(f.opened, it)
	ch := make(chan string, 1)
	f.chans[it.ID] = ch
	return ch, nil
}

func (f *fakeSink) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.opened)
}

// answerVia delivers an answer (or closes without one) on the channel a prior
// Open returned for id.
func (f *fakeSink) answerVia(id, ans string, deliver bool) {
	f.mu.Lock()
	ch := f.chans[id]
	f.mu.Unlock()
	if ch == nil {
		return
	}
	if deliver {
		ch <- ans
	}
	close(ch)
}

// newBridge wires an interactionBridge to a fake sink + a recording answer fn,
// mirroring how Run constructs it.
func newBridge(sink runner.InteractionSink, answer func(iid, ans string)) *interactionBridge {
	return &interactionBridge{
		ctx:     context.Background(),
		sinks:   sink,
		answer:  answer,
		seen:    map[string]bool{},
		jobID:   "j1",
		hasSink: sink != nil,
	}
}

// openFrame builds the raw interaction body the hub passes to OnInteraction.
func openFrame(id, prompt string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"id": id, "type": "question", "prompt": prompt})
	return b
}

// TestBridgeOpenInjects: an open frame calls req.Interactions.Open exactly once.
func TestBridgeOpenInjects(t *testing.T) {
	sink := newFakeSink()
	b := newBridge(sink, func(string, string) {})
	b.handle("open", openFrame("i1", "need input"))
	if sink.openCount() != 1 {
		t.Fatalf("Open call count = %d, want 1", sink.openCount())
	}
}

// TestBridgeAnswerRoundTrip: a host answer on the channel is forwarded back via
// the answer fn (== hub.Answer over WS).
func TestBridgeAnswerRoundTrip(t *testing.T) {
	sink := newFakeSink()
	var (
		mu       sync.Mutex
		gotIID   string
		gotAns   string
		answered = make(chan struct{})
	)
	b := newBridge(sink, func(iid, ans string) {
		mu.Lock()
		gotIID, gotAns = iid, ans
		mu.Unlock()
		close(answered)
	})
	b.handle("open", openFrame("i1", "need input"))
	sink.answerVia("i1", "yes", true)

	select {
	case <-answered:
	case <-time.After(2 * time.Second):
		t.Fatal("answer was never forwarded")
	}
	mu.Lock()
	defer mu.Unlock()
	if gotIID != "i1" || gotAns != "yes" {
		t.Fatalf("forwarded answer = (%q,%q), want (i1,yes)", gotIID, gotAns)
	}
}

// TestBridgeJobEndsNoAnswer: when the channel closes WITHOUT a value (job ended /
// ctx cancelled), the bridge must NOT forward an answer.
func TestBridgeJobEndsNoAnswer(t *testing.T) {
	sink := newFakeSink()
	var calls atomic.Int32
	b := newBridge(sink, func(string, string) { calls.Add(1) })
	b.handle("open", openFrame("i1", "need input"))
	sink.answerVia("i1", "", false) // close without a value

	time.Sleep(100 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("answer forwarded %d times on a closed-no-value channel, want 0", got)
	}
}

// TestBridgeDuplicateOpenIdempotent: a re-sent open for the same id only injects
// (and forwards) once (seen dedupe).
func TestBridgeDuplicateOpenIdempotent(t *testing.T) {
	sink := newFakeSink()
	b := newBridge(sink, func(string, string) {})
	b.handle("open", openFrame("i1", "need input"))
	b.handle("open", openFrame("i1", "need input")) // duplicate
	if sink.openCount() != 1 {
		t.Fatalf("Open called %d times for a duplicate open, want 1", sink.openCount())
	}
}

// TestBridgeNonOpenIgnored: answered/cancelled actions are accepted and ignored
// (the host owns its own interaction record); they never call Open.
func TestBridgeNonOpenIgnored(t *testing.T) {
	sink := newFakeSink()
	b := newBridge(sink, func(string, string) {})
	b.handle("answered", openFrame("i1", "x"))
	b.handle("cancelled", openFrame("i1", "x"))
	if sink.openCount() != 0 {
		t.Fatalf("non-open action triggered %d Open calls, want 0", sink.openCount())
	}
}

// TestBridgeNilSink: a job with no host interaction sink ignores interactions
// without panicking.
func TestBridgeNilSink(t *testing.T) {
	b := newBridge(nil, func(string, string) {})
	b.handle("open", openFrame("i1", "x")) // must not panic
}

// TestRunCtxCancelSendsCancelFrame: a host ctx cancel forwards a cancel frame to
// the worker (P2) in addition to returning ctx.Err().
func TestRunCtxCancelSendsCancelFrame(t *testing.T) {
	h := &fakeHub{}
	r := newRunnerWithHub(h)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(ctx, runner.Request{JobID: "j1", Forward: &runner.Forward{}})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.getSink() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return on ctx cancel")
	}
	if h.cancelCount() != 1 {
		t.Fatalf("hub.Cancel called %d times on ctx cancel, want 1", h.cancelCount())
	}
}
