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

	"github.com/inhere/gofer/internal/ptyrelay"
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
	dispatched    wsproto.Dispatch
	// targetWorker records the workerID the runner resolved (the same id flows
	// into RegisterSink/Dispatch/Deregister). It proves Forward.WorkerID routing
	// vs the r.workerID fallback (P2).
	targetWorker string

	instanceID        string
	liveInstanceCalls int
	cancelCalls       int // count of Cancel(workerID, jobID) calls
}

func (h *fakeHub) LiveInstance(workerID string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.liveInstanceCalls++
	h.targetWorker = workerID
	if h.instanceID == "" {
		return "", false
	}
	return h.instanceID, true
}

func (h *fakeHub) RegisterSink(workerID, _ string, sk wshub.JobSink) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, "register")
	h.targetWorker = workerID
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

func (h *fakeHub) Dispatch(workerID string, d wsproto.Dispatch) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, "dispatch")
	h.dispatchedCmd = d.Cmd
	h.dispatched = d
	h.targetWorker = workerID
	return h.dispatchErr
}

func (h *fakeHub) target() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.targetWorker
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

func (h *fakeHub) liveInstanceCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.liveInstanceCalls
}

func (h *fakeHub) dispatchedFrame() wsproto.Dispatch {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dispatched
}

func (h *fakeHub) getSink() wshub.JobSink {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sink
}

type fakeNonceStore struct {
	mu     sync.Mutex
	issued []ptyrelay.NonceBinding
}

func (s *fakeNonceStore) Issue(b ptyrelay.NonceBinding) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issued = append(s.issued, b)
	return "nonce-1"
}

func (s *fakeNonceStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.issued)
}

// closedChan is a shared pre-closed chan the fake returns from Done by default
// (pending/missing relay semantics — the host proceeds at once).
var closedChan = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

type fakeRelayRegistry struct {
	mu       sync.Mutex
	prepared []ptyrelay.RelayBinding
	closed   []string
	// doneCh, when non-nil, is returned by Done so a test can hold an interactive
	// Run in its drain-wait (open chan) and release it (close) to assert ordering.
	// nil → a pre-closed chan (pending/missing relay = proceed immediately).
	doneCh chan struct{}
}

func (r *fakeRelayRegistry) Prepare(b ptyrelay.RelayBinding) *ptyrelay.RelayEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prepared = append(r.prepared, b)
	return &ptyrelay.RelayEntry{Binding: b, State: ptyrelay.RelayPendingWorker}
}

func (r *fakeRelayRegistry) Done(string) <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.doneCh != nil {
		return r.doneCh
	}
	return closedChan
}

func (r *fakeRelayRegistry) Close(jobID, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = append(r.closed, jobID+":"+reason)
}

func (r *fakeRelayRegistry) firstPrepared() (ptyrelay.RelayBinding, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.prepared) == 0 {
		return ptyrelay.RelayBinding{}, false
	}
	return r.prepared[0], true
}

func (r *fakeRelayRegistry) closedSnapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.closed...)
}

// newRunnerWithHub builds a Runner wired to a fake hub (bypasses New's concrete
// *wshub.Hub requirement, exercising the same code path via the dispatcher
// interface).
func newRunnerWithHub(h dispatcher) *Runner {
	return &Runner{name: "remote-w1", workerID: "w1", hub: h, nowUnix: func() int64 { return 100 }}
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

// runToResultWithWorker drives a Run to completion (feeding a done result once the
// sink registers) and returns the workerID the hub was dispatched to. It is the
// shared driver for the two P2 dynamic-routing cases.
func runToResultWithWorker(t *testing.T, r *Runner, h *fakeHub, f *runner.Forward) string {
	t.Helper()
	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(context.Background(), runner.Request{JobID: "j1", Forward: f})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.getSink() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	sink := h.getSink()
	if sink == nil {
		t.Fatal("sink never registered")
	}
	sink.Finish(wsproto.Result{JobID: "j1", Status: "done", ExitCode: 0})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Finish")
	}
	return h.target()
}

// TestRunForwardWorkerIDRouting (P2 D3): a non-empty Forward.WorkerID is the
// dispatch target (dynamic routing), overriding the runner's configured default.
func TestRunForwardWorkerIDRouting(t *testing.T) {
	h := &fakeHub{}
	r := newRunnerWithHub(h) // configured default worker is "w1"
	got := runToResultWithWorker(t, r, h, &runner.Forward{WorkerID: "w-selected"})
	if got != "w-selected" {
		t.Fatalf("dispatched to %q, want w-selected (Forward.WorkerID wins)", got)
	}
}

// TestRunForwardWorkerIDFallback (P2 D4): an empty Forward.WorkerID falls back to
// the runner's configured default worker (r.workerID).
func TestRunForwardWorkerIDFallback(t *testing.T) {
	h := &fakeHub{}
	r := newRunnerWithHub(h)                                 // configured default worker is "w1"
	got := runToResultWithWorker(t, r, h, &runner.Forward{}) // no WorkerID
	if got != "w1" {
		t.Fatalf("dispatched to %q, want w1 (fallback to r.workerID)", got)
	}
}

// TestRunNoTargetWorker: a runner with no default worker and an empty
// Forward.WorkerID has no target and errors immediately (no dispatch).
func TestRunNoTargetWorker(t *testing.T) {
	h := &fakeHub{}
	r := &Runner{name: "remote-w1", workerID: "", hub: h} // no default binding
	res := r.Run(context.Background(), runner.Request{JobID: "j1", Forward: &runner.Forward{}})
	if res.ExitCode != -1 || res.Err == nil {
		t.Fatalf("no target worker should yield ExitCode -1 + err, got %+v", res)
	}
	if len(h.snapshotCalls()) != 0 {
		t.Fatalf("no hub calls should be made without a target, got %v", h.snapshotCalls())
	}
}

func TestRunInteractiveIssuesRelayNonceAndPreparesRegistry(t *testing.T) {
	h := &fakeHub{instanceID: "inst-1"}
	nonces := &fakeNonceStore{}
	relays := &fakeRelayRegistry{}
	r := newRunnerWithHub(h)
	r.nonceStore = nonces
	r.relayRegistry = relays

	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(context.Background(), runner.Request{
			JobID: "j1",
			Forward: &runner.Forward{
				WorkerID: "w-selected", Interactive: true, Cols: 120, Rows: 40,
			},
		})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.getSink() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	sink := h.getSink()
	if sink == nil {
		t.Fatal("sink never registered")
	}
	sink.Finish(wsproto.Result{JobID: "j1", Status: "done", ExitCode: 0})
	select {
	case res := <-done:
		if res.ExitCode != 0 || res.Err != nil {
			t.Fatalf("result = %+v, want clean done", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Finish")
	}

	d := h.dispatchedFrame()
	if d.RelayNonce == "" {
		t.Fatal("interactive Dispatch.RelayNonce is empty")
	}
	if d.RelayNonce != "nonce-1" || !d.Interactive || d.Cols != 120 || d.Rows != 40 {
		t.Fatalf("dispatch = %+v", d)
	}
	if h.liveInstanceCount() != 1 {
		t.Fatalf("LiveInstance calls = %d, want 1", h.liveInstanceCount())
	}
	if nonces.count() != 1 {
		t.Fatalf("nonce Issue count = %d, want 1", nonces.count())
	}
	prepared, ok := relays.firstPrepared()
	if !ok {
		t.Fatal("registry Prepare was not called")
	}
	if prepared.WorkerID != "w-selected" || prepared.InstanceID != "inst-1" || prepared.JobID != "j1" || prepared.Nonce != "nonce-1" || prepared.PtySessionID == "" {
		t.Fatalf("prepared binding = %+v", prepared)
	}
	if got := relays.closedSnapshot(); len(got) != 1 || got[0] != "j1:worker_result" {
		t.Fatalf("relay close calls = %v, want [j1:worker_result]", got)
	}
}

func TestRunInteractiveClosesRelayOnWorkerLostAndCancel(t *testing.T) {
	t.Run("worker_lost", func(t *testing.T) {
		h := &fakeHub{instanceID: "inst-1"}
		relays := &fakeRelayRegistry{}
		r := newRunnerWithHub(h)
		r.nonceStore = &fakeNonceStore{}
		r.relayRegistry = relays

		done := make(chan runner.Result, 1)
		go func() {
			done <- r.Run(context.Background(), runner.Request{
				JobID: "j1", Forward: &runner.Forward{WorkerID: "w1", Interactive: true},
			})
		}()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && h.getSink() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		sink := h.getSink()
		if sink == nil {
			t.Fatal("sink never registered")
		}
		sink.OnDisconnect(errors.New("worker disconnected"))
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return on worker-lost")
		}
		if got := relays.closedSnapshot(); len(got) != 1 || got[0] != "j1:worker_lost" {
			t.Fatalf("relay close calls = %v, want [j1:worker_lost]", got)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		h := &fakeHub{instanceID: "inst-1"}
		relays := &fakeRelayRegistry{}
		r := newRunnerWithHub(h)
		r.nonceStore = &fakeNonceStore{}
		r.relayRegistry = relays
		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan runner.Result, 1)
		go func() {
			done <- r.Run(ctx, runner.Request{
				JobID: "j1", Forward: &runner.Forward{WorkerID: "w1", Interactive: true},
			})
		}()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && h.getSink() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		if h.getSink() == nil {
			t.Fatal("sink never registered")
		}
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return on cancel")
		}
		if got := relays.closedSnapshot(); len(got) != 1 || got[0] != "j1:cancelled" {
			t.Fatalf("relay close calls = %v, want [j1:cancelled]", got)
		}
	})
}

// TestRunInteractiveDispatchCarriesPtySessionID (T1): an interactive Dispatch
// echoes the host-minted ptySessionID (the same one Prepare/Issue bound), so the
// worker can replay it in its pty-connect hello for the serve-side strong check.
// A non-interactive Dispatch carries an empty PtySessionID.
func TestRunInteractiveDispatchCarriesPtySessionID(t *testing.T) {
	t.Run("interactive", func(t *testing.T) {
		h := &fakeHub{instanceID: "inst-1"}
		relays := &fakeRelayRegistry{}
		r := newRunnerWithHub(h)
		r.nonceStore = &fakeNonceStore{}
		r.relayRegistry = relays

		done := make(chan runner.Result, 1)
		go func() {
			done <- r.Run(context.Background(), runner.Request{
				JobID:   "j1",
				Forward: &runner.Forward{WorkerID: "w-selected", Interactive: true},
			})
		}()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && h.getSink() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		sink := h.getSink()
		if sink == nil {
			t.Fatal("sink never registered")
		}
		sink.Finish(wsproto.Result{JobID: "j1", Status: "done", ExitCode: 0})
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return after Finish")
		}

		d := h.dispatchedFrame()
		if d.PtySessionID == "" {
			t.Fatal("interactive Dispatch.PtySessionID is empty, want the host-minted id")
		}
		prepared, ok := relays.firstPrepared()
		if !ok {
			t.Fatal("registry Prepare was not called")
		}
		if d.PtySessionID != prepared.PtySessionID {
			t.Fatalf("Dispatch.PtySessionID = %q, want == prepared binding %q", d.PtySessionID, prepared.PtySessionID)
		}
	})

	t.Run("non_interactive", func(t *testing.T) {
		h := &fakeHub{instanceID: "inst-1"}
		r := newRunnerWithHub(h)
		r.nonceStore = &fakeNonceStore{}
		r.relayRegistry = &fakeRelayRegistry{}
		done := make(chan runner.Result, 1)
		go func() {
			done <- r.Run(context.Background(), runner.Request{JobID: "j1", Forward: &runner.Forward{}})
		}()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && h.getSink() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		sink := h.getSink()
		if sink == nil {
			t.Fatal("sink never registered")
		}
		sink.Finish(wsproto.Result{JobID: "j1", Status: "done", ExitCode: 0})
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return after Finish")
		}
		if got := h.dispatchedFrame().PtySessionID; got != "" {
			t.Fatalf("non-interactive Dispatch.PtySessionID = %q, want empty", got)
		}
	})
}

func TestRunNonInteractiveDoesNotIssueRelayNonce(t *testing.T) {
	h := &fakeHub{instanceID: "inst-1"}
	nonces := &fakeNonceStore{}
	relays := &fakeRelayRegistry{}
	r := newRunnerWithHub(h)
	r.nonceStore = nonces
	r.relayRegistry = relays

	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(context.Background(), runner.Request{JobID: "j1", Forward: &runner.Forward{}})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.getSink() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	sink := h.getSink()
	if sink == nil {
		t.Fatal("sink never registered")
	}
	sink.Finish(wsproto.Result{JobID: "j1", Status: "done", ExitCode: 0})
	select {
	case res := <-done:
		if res.ExitCode != 0 || res.Err != nil {
			t.Fatalf("result = %+v, want clean done", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Finish")
	}
	if got := h.dispatchedFrame().RelayNonce; got != "" {
		t.Fatalf("non-interactive RelayNonce = %q, want empty", got)
	}
	if h.liveInstanceCount() != 0 {
		t.Fatalf("LiveInstance calls = %d, want 0", h.liveInstanceCount())
	}
	if nonces.count() != 0 {
		t.Fatalf("nonce Issue count = %d, want 0", nonces.count())
	}
	if _, ok := relays.firstPrepared(); ok {
		t.Fatal("registry Prepare called for non-interactive run")
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

// TestRunOutcomeReturned (P4-a): the hub delivers an Outcome frame (via the
// sink's OnOutcome) just before the terminal result; Run must return it on
// runner.Result.Outcome with all 4 captured fields + Source="worker:<id>".
func TestRunOutcomeReturned(t *testing.T) {
	h := &fakeHub{}
	r := newRunnerWithHub(h) // configured worker is "w1"
	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(context.Background(), runner.Request{JobID: "j1", Forward: &runner.Forward{}})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.getSink() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	sink := h.getSink()
	if sink == nil {
		t.Fatal("sink never registered")
	}
	// Outcome frame BEFORE the result frame (matches the worker's send order).
	sink.OnOutcome(wsproto.Outcome{
		JobID:           "j1",
		RenderedCommand: `{"command":"echo","args":["hi"]}`,
		ResultJSON:      `{"ok":true}`,
		DiffSummary:     " a.txt | 1 +",
		Artifacts:       json.RawMessage(`[{"name":"out.bin","size":3,"mtime":1}]`),
	})
	sink.Finish(wsproto.Result{JobID: "j1", Status: "done", ExitCode: 0})

	select {
	case res := <-done:
		if res.Outcome == nil {
			t.Fatal("Result.Outcome is nil, want the worker-captured outcome")
		}
		o := res.Outcome
		if o.RenderedCommand != `{"command":"echo","args":["hi"]}` {
			t.Fatalf("rendered_command = %q", o.RenderedCommand)
		}
		if o.ResultJSON != `{"ok":true}` {
			t.Fatalf("result_json = %q", o.ResultJSON)
		}
		if o.DiffSummary != " a.txt | 1 +" {
			t.Fatalf("diff_summary = %q", o.DiffSummary)
		}
		if string(o.Artifacts) != `[{"name":"out.bin","size":3,"mtime":1}]` {
			t.Fatalf("artifacts = %q", string(o.Artifacts))
		}
		if o.Source != "worker:w1" {
			t.Fatalf("source = %q, want worker:w1", o.Source)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Finish")
	}
}

// TestRunNoOutcomeFrame (P4-a 回归红线): an OLD worker sends NO outcome frame —
// only the result — so Run returns a nil Outcome and the host job outcome stays
// empty (a missing/unknown frame must never break the result path).
func TestRunNoOutcomeFrame(t *testing.T) {
	h := &fakeHub{}
	r := newRunnerWithHub(h)
	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(context.Background(), runner.Request{JobID: "j1", Forward: &runner.Forward{}})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.getSink() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	sink := h.getSink()
	if sink == nil {
		t.Fatal("sink never registered")
	}
	// No OnOutcome — straight to Finish (old worker behaviour).
	sink.Finish(wsproto.Result{JobID: "j1", Status: "done", ExitCode: 0})
	select {
	case res := <-done:
		if res.Outcome != nil {
			t.Fatalf("Result.Outcome = %+v, want nil (no outcome frame from old worker)", res.Outcome)
		}
		if res.ExitCode != 0 || res.Err != nil {
			t.Fatalf("result = %+v, want clean done", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Finish")
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

// TestRunAtCapacity (WP3 §5.4): a Dispatch rejected with ErrWorkerAtCapacity
// surfaces as a non-nil Run error so the job service classifies the job failed
// with a clear "worker at capacity" message (queueing is WP4).
func TestRunAtCapacity(t *testing.T) {
	r := newRunnerWithHub(&fakeHub{dispatchErr: wshub.ErrWorkerAtCapacity})
	res := r.Run(context.Background(), runner.Request{JobID: "j1", Forward: &runner.Forward{}})
	if res.ExitCode != -1 {
		t.Fatalf("exit = %d, want -1", res.ExitCode)
	}
	if !errors.Is(res.Err, wshub.ErrWorkerAtCapacity) {
		t.Fatalf("err = %v, want ErrWorkerAtCapacity", res.Err)
	}
}

// TestRunWorkerLostFailsJob (WP3 §5.3): the hub signals worker-lost via the
// sink's OnDisconnect while Run is waiting; Run must return ExitCode -1 with the
// "worker disconnected" error and NO ctx error, so the job service's classify
// maps it to StatusFailed and the text flows verbatim into jobs.error.
func TestRunWorkerLostFailsJob(t *testing.T) {
	h := &fakeHub{}
	r := newRunnerWithHub(h)
	// A live (non-cancelled, no-deadline) ctx: classify must see res.Err only.
	ctx := context.Background()
	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(ctx, runner.Request{JobID: "j1", Forward: &runner.Forward{}})
	}()
	// Wait for the sink to register, then signal worker-lost.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.getSink() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	sink := h.getSink()
	if sink == nil {
		t.Fatal("sink never registered")
	}
	sink.OnDisconnect(errors.New("worker disconnected"))

	select {
	case res := <-done:
		if res.ExitCode != -1 {
			t.Fatalf("exit = %d, want -1", res.ExitCode)
		}
		if res.Err == nil || res.Err.Error() != "worker disconnected" {
			t.Fatalf("err = %v, want 'worker disconnected'", res.Err)
		}
		if ctx.Err() != nil {
			t.Fatalf("ctx must stay live so classify maps to failed (got %v)", ctx.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return on worker-lost")
	}
	// No cancel frame should be sent (the worker is gone, not cancelled).
	if h.cancelCount() != 0 {
		t.Fatalf("worker-lost must not send a cancel frame, got %d", h.cancelCount())
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

// newInteractiveRunner builds a Runner wired to h + relays for the T6 interactive
// drain-wait tests (a fake nonce store, so the interactive setup succeeds).
func newInteractiveRunner(h dispatcher, relays relayPreparer) *Runner {
	r := newRunnerWithHub(h)
	r.nonceStore = &fakeNonceStore{}
	r.relayRegistry = relays
	return r
}

// waitForSink blocks until the runner registered its sink (or fails the test).
func waitForSink(t *testing.T, h *fakeHub) wshub.JobSink {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.getSink() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	sink := h.getSink()
	if sink == nil {
		t.Fatal("sink never registered")
	}
	return sink
}

// TestRunInteractiveWaitsRelayDoneBeforeResult (T6, D-P2-6): an interactive Run
// does not return its worker result until the serve relay signals drain-complete
// (Done closes), so the browser sees the pty tail before the job finishes.
func TestRunInteractiveWaitsRelayDoneBeforeResult(t *testing.T) {
	h := &fakeHub{instanceID: "inst-1"}
	relays := &fakeRelayRegistry{doneCh: make(chan struct{})} // open → Run must block on it
	r := newInteractiveRunner(h, relays)

	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(context.Background(), runner.Request{
			JobID: "j1", Forward: &runner.Forward{WorkerID: "w1", Interactive: true},
		})
	}()
	sink := waitForSink(t, h)
	sink.Finish(wsproto.Result{JobID: "j1", Status: "done", ExitCode: 0})

	// Result landed, but Done is still open → Run must be parked in the drain-wait.
	select {
	case res := <-done:
		t.Fatalf("Run returned before relay Done closed: %+v", res)
	case <-time.After(200 * time.Millisecond):
	}
	close(relays.doneCh) // drain complete → Run proceeds
	select {
	case res := <-done:
		if res.ExitCode != 0 || res.Err != nil {
			t.Fatalf("result = %+v, want clean done", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after relay Done closed")
	}
}

// TestRunInteractivePendingDoneReturnsImmediately (T6): a pending/missing relay's
// Done is a pre-closed chan, so a non-attached interactive job finishes at once
// (no spurious grace wait).
func TestRunInteractivePendingDoneReturnsImmediately(t *testing.T) {
	h := &fakeHub{instanceID: "inst-1"}
	relays := &fakeRelayRegistry{} // doneCh nil → pre-closed (pending/missing)
	r := newInteractiveRunner(h, relays)

	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(context.Background(), runner.Request{
			JobID: "j1", Forward: &runner.Forward{WorkerID: "w1", Interactive: true},
		})
	}()
	sink := waitForSink(t, h)
	sink.Finish(wsproto.Result{JobID: "j1", Status: "done", ExitCode: 0})
	select {
	case res := <-done:
		if res.ExitCode != 0 || res.Err != nil {
			t.Fatalf("result = %+v, want immediate clean done", res)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("interactive Run with pre-closed Done did not return immediately")
	}
}

// TestRunInteractiveGraceFallback (T6): if the relay never drains (Done stays
// open), an interactive Run still finishes after hostCancelGrace.
func TestRunInteractiveGraceFallback(t *testing.T) {
	old := hostCancelGrace
	hostCancelGrace = 50 * time.Millisecond
	defer func() { hostCancelGrace = old }()

	h := &fakeHub{instanceID: "inst-1"}
	relays := &fakeRelayRegistry{doneCh: make(chan struct{})} // never closed → grace must fire
	r := newInteractiveRunner(h, relays)

	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(context.Background(), runner.Request{
			JobID: "j1", Forward: &runner.Forward{WorkerID: "w1", Interactive: true},
		})
	}()
	sink := waitForSink(t, h)
	sink.Finish(wsproto.Result{JobID: "j1", Status: "done", ExitCode: 0})
	select {
	case res := <-done:
		if res.ExitCode != 0 || res.Err != nil {
			t.Fatalf("result = %+v, want clean done after grace", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return via grace fallback")
	}
}

// TestRunInteractiveCancelWaitsRelayDone (T6, D-P2-6): on a host cancel the runner
// sends the cancel frame then waits for the relay to drain (Done) before returning
// ctx.Err — so a cancel-triggered pty tail (e.g. a sentinel) still reaches the
// browser.
func TestRunInteractiveCancelWaitsRelayDone(t *testing.T) {
	h := &fakeHub{instanceID: "inst-1"}
	relays := &fakeRelayRegistry{doneCh: make(chan struct{})} // open → cancel path blocks on it
	r := newInteractiveRunner(h, relays)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(ctx, runner.Request{JobID: "j1", Forward: &runner.Forward{WorkerID: "w1", Interactive: true}})
	}()
	waitForSink(t, h)
	cancel()

	// Cancel frame is sent, but Run must park in the 3-way wait (Done open, no lost).
	select {
	case res := <-done:
		t.Fatalf("Run returned before relay Done closed on cancel: %+v", res)
	case <-time.After(200 * time.Millisecond):
	}
	if h.cancelCount() != 1 {
		t.Fatalf("hub.Cancel = %d, want 1", h.cancelCount())
	}
	close(relays.doneCh)
	select {
	case res := <-done:
		if res.Err == nil {
			t.Fatal("cancel should surface ctx.Err")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after relay Done closed on cancel")
	}
	if got := relays.closedSnapshot(); len(got) != 1 || got[0] != "j1:cancelled" {
		t.Fatalf("relay close = %v, want [j1:cancelled]", got)
	}
}

// TestRunInteractiveCancelResolvesOnWorkerLost (T6): during the cancel drain-wait,
// a worker-lost drop resolves the 3-way select even if Done never fires.
func TestRunInteractiveCancelResolvesOnWorkerLost(t *testing.T) {
	h := &fakeHub{instanceID: "inst-1"}
	relays := &fakeRelayRegistry{doneCh: make(chan struct{})} // never closed → stuck drain
	r := newInteractiveRunner(h, relays)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(ctx, runner.Request{JobID: "j1", Forward: &runner.Forward{WorkerID: "w1", Interactive: true}})
	}()
	sink := waitForSink(t, h)
	cancel()
	time.Sleep(50 * time.Millisecond) // let Run enter the 3-way wait
	sink.OnDisconnect(errors.New("worker disconnected"))
	select {
	case res := <-done:
		if res.Err == nil {
			t.Fatal("cancel should surface ctx.Err")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not resolve on worker-lost during cancel drain-wait")
	}
}

// TestRunNonInteractiveIgnoresRelayDone (T6, G023): a non-interactive Run never
// consults the relay Done — it returns immediately even when Done is held open.
func TestRunNonInteractiveIgnoresRelayDone(t *testing.T) {
	h := &fakeHub{}
	relays := &fakeRelayRegistry{doneCh: make(chan struct{})} // open; must be ignored
	r := newInteractiveRunner(h, relays)

	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(context.Background(), runner.Request{JobID: "j1", Forward: &runner.Forward{}}) // non-interactive
	}()
	sink := waitForSink(t, h)
	sink.Finish(wsproto.Result{JobID: "j1", Status: "done", ExitCode: 0})
	select {
	case res := <-done:
		if res.ExitCode != 0 || res.Err != nil {
			t.Fatalf("result = %+v, want immediate clean done", res)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("non-interactive Run waited on relay Done (must return immediately)")
	}
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
