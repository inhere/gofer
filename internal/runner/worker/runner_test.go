package worker

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
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

func (h *fakeHub) snapshotCalls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
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
