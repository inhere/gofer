package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/job"
	ptyrunner "github.com/inhere/gofer/internal/runner/pty"
	"github.com/inhere/gofer/internal/wsproto"
)

// stubJobs is a fake worker.Jobs for unit-testing handleDispatch without a real
// job.Service: it returns a configurable Submit result/error and a terminal Wait.
type stubJobs struct {
	submitErr   error
	submitID    string
	submitDir   string
	waitResult  job.JobResult
	waitOK      bool
	getResult   job.JobResult
	getOK       bool
	gotProject  string
	gotRunner   string
	// gotInteractive/gotCols/gotRows capture the T5 interactive projection so a test
	// can assert the worker forwards Interactive+window to its own job.Service.
	gotInteractive bool
	gotCols        int
	gotRows        int
	submitCalls    int

	// interactions returned by GetInteractions (drives the worker→hub open bridge).
	interactions []job.Interaction
	// recorded inbound cancel/answer the worker delivers to the local job.
	cancelledID    string
	answeredJob    string
	answeredIID    string
	answeredAnswer string

	mu sync.Mutex
}

func (s *stubJobs) Submit(req job.JobRequest) (job.JobResult, error) {
	s.mu.Lock()
	s.submitCalls++
	s.gotProject = req.ProjectKey
	s.gotRunner = req.Runner
	s.gotInteractive = req.Interactive
	s.gotCols, s.gotRows = req.Cols, req.Rows
	s.mu.Unlock()
	if s.submitErr != nil {
		return job.JobResult{}, s.submitErr
	}
	return job.JobResult{ID: s.submitID, ResultDir: s.submitDir, Status: job.StatusRunning}, nil
}
func (s *stubJobs) Get(string) (job.JobResult, bool)  { return s.getResult, s.getOK }
func (s *stubJobs) Wait(string) (job.JobResult, bool) { return s.waitResult, s.waitOK }

func (s *stubJobs) Cancel(id string) error {
	s.mu.Lock()
	s.cancelledID = id
	s.mu.Unlock()
	return nil
}

func (s *stubJobs) GetInteractions(string) ([]job.Interaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]job.Interaction(nil), s.interactions...), nil
}

func (s *stubJobs) AnswerInteraction(jobID, iid, answer string) (job.Interaction, error) {
	s.mu.Lock()
	s.answeredJob, s.answeredIID, s.answeredAnswer = jobID, iid, answer
	s.mu.Unlock()
	return job.Interaction{ID: iid, Status: job.InteractionAnswered, Answer: answer}, nil
}

func (s *stubJobs) cancelled() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelledID
}

func (s *stubJobs) answered() (string, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.answeredJob, s.answeredIID, s.answeredAnswer
}

// connectClientToFakeHub stands up a minimal hub that only accepts the WS, reads
// the register, replies registered{accepted}, and returns the server-side conn
// so the test can read the frames the worker pushes. The worker.Client is wired
// to the given stubJobs.
func connectClientToFakeHub(t *testing.T, jobs Jobs) (*Client, chan wsproto.Envelope) {
	t.Helper()
	frames := make(chan wsproto.Envelope, 16)
	// Use a stdlib handler (not rux) for this client-only dispatch test — the
	// worker dials a plain WS endpoint here. (Real upgrades through rux's
	// responseWriter are covered by wshub's spike + the worker e2e; rux v2.0.2
	// flushes the 101 on Hijack natively, so no adapter is involved.)
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		ctx := req.Context()
		var reg wsproto.Envelope
		if err := wsjson.Read(ctx, conn, &reg); err != nil {
			return
		}
		_ = wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeRegistered, Payload: mustRaw(wsproto.Registered{Accepted: true})})
		_ = wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeDispatch, JobID: "d1", Payload: mustRaw(wsproto.Dispatch{
			JobID: "d1", ProjectKey: "alpha", Agent: "exec", Runner: "local", Cmd: []string{"echo", "hi"},
		})})
		for {
			var env wsproto.Envelope
			if err := wsjson.Read(ctx, conn, &env); err != nil {
				return
			}
			frames <- env
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workers/connect"
	cl := New(Config{WorkerID: "w1", URLs: []string{wsURL}, Token: "t"}, jobs)
	cl.pollInterval = 20 * time.Millisecond
	return cl, frames
}

func TestHandleDispatchSubmitsLocal(t *testing.T) {
	root := t.TempDir()
	jobs := &stubJobs{
		submitID:   "local-1",
		submitDir:  root + "/alpha/local-1",
		getResult:  job.JobResult{Status: job.StatusDone},
		getOK:      true,
		waitResult: job.JobResult{Status: job.StatusDone, ExitCode: 0},
		waitOK:     true,
	}
	client, frames := connectClientToFakeHub(t, jobs)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()

	// The fake hub auto-dispatches d1; wait for the worker to push a result frame.
	res := waitForResult(t, frames, "d1")
	if res.Status != job.StatusDone || res.ExitCode != 0 {
		t.Fatalf("result = %+v, want done/0", res)
	}
	jobs.mu.Lock()
	gotRunner, gotProject := jobs.gotRunner, jobs.gotProject
	jobs.mu.Unlock()
	if gotRunner != builtinLocalRunner {
		t.Fatalf("submit runner = %q, want local", gotRunner)
	}
	if gotProject != "alpha" {
		t.Fatalf("submit project = %q, want alpha", gotProject)
	}
}

func TestHandleDispatchValidateFail(t *testing.T) {
	// Submit fails (worker's local config rejects the agent): the worker must push
	// result{failed} and NOT proceed to Wait.
	jobs := &stubJobs{submitErr: errors.New("agent not allowed")}
	client, frames := connectClientToFakeHub(t, jobs)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()

	res := waitForResult(t, frames, "d1")
	if res.Status != job.StatusFailed {
		t.Fatalf("result status = %q, want failed", res.Status)
	}
	if res.Error == "" {
		t.Fatal("expected an error message on the failed result")
	}
}

func TestRegisterRejected(t *testing.T) {
	// A hub that rejects the register → runSession returns the rejection error.
	// (Run, the reconnect supervisor, would back off and RETRY a rejection — the
	// config may be fixed — and only returns on ctx cancel; §5.2. So the rejection
	// semantics are asserted at the session granularity.)
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		ctx := req.Context()
		var reg wsproto.Envelope
		_ = wsjson.Read(ctx, conn, &reg)
		_ = wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeRegistered, Payload: mustRaw(wsproto.Registered{Accepted: false, Reason: "nope"})})
		// Close like the real hub does after a binding rejection, so the client's
		// graceful close handshake completes promptly (no 5s close-handshake wait).
		_ = conn.Close(websocket.StatusPolicyViolation, "binding")
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workers/connect"
	cl := New(Config{WorkerID: "w1", URLs: []string{wsURL}, Token: "t"}, &stubJobs{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registered, err := cl.runSession(ctx, wsURL)
	if registered {
		t.Fatal("a rejected register must not report registered=true")
	}
	if err == nil || !strings.Contains(err.Error(), "register rejected") {
		t.Fatalf("expected register rejected error, got %v", err)
	}
}

// TestOutcomeFrameSessionID proves the worker's outcomeFrame copies the local
// terminal JobResult.SessionID (P1 已捕获/注入) into the回传 wsproto.Outcome.SessionID
// (P3), and that a session_id alone is enough to send the frame (旧逻辑只看产出字段)。
func TestOutcomeFrameSessionID(t *testing.T) {
	// 仅有 session_id（无 rendered/result/diff/artifacts）也要回传。
	o, send := outcomeFrame("j1", job.JobResult{SessionID: "sess-xyz"})
	if !send {
		t.Fatalf("a session_id-only outcome must still be sent")
	}
	if o.SessionID != "sess-xyz" || o.JobID != "j1" {
		t.Fatalf("outcomeFrame lost session_id/job_id: %+v", o)
	}

	// 完全空 → 不发帧（回归红线：旧 worker 行为不变）。
	if _, send := outcomeFrame("j2", job.JobResult{}); send {
		t.Fatalf("an empty JobResult must not produce a frame")
	}
}

// dialLiveClient stands up a bare ws server that captures every frame the worker
// writes, dials it, and wires cl.conn directly so a test can call handleDispatch
// in isolation (no Run/recvLoop). It returns the client, the captured-frames chan,
// and the hub session URL (ending /v1/workers/connect, so an interactive dispatch
// derives its pty-connect URL from it).
func dialLiveClient(t *testing.T, jobs Jobs) (*Client, chan wsproto.Envelope, string) {
	t.Helper()
	frames := make(chan wsproto.Envelope, 32)
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		for {
			var env wsproto.Envelope
			if rerr := wsjson.Read(req.Context(), conn, &env); rerr != nil {
				return
			}
			frames <- env
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workers/connect"
	cl := New(Config{WorkerID: "w1", URLs: []string{wsURL}, Token: "t"}, jobs)
	cl.pollInterval = 10 * time.Millisecond
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	cl.conn = conn
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })
	return cl, frames, wsURL
}

// TestHandleDispatchInteractiveFailFast (T5, D-P2-4): an interactive dispatch with
// no relay credentials can never attach; the worker must report failed WITHOUT
// submitting a bare pty job.
func TestHandleDispatchInteractiveFailFast(t *testing.T) {
	jobs := &stubJobs{submitID: "local-1"}
	cl, frames, url := dialLiveClient(t, jobs)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cl.handleDispatch(ctx, url, wsproto.Dispatch{JobID: "d1", Interactive: true}) // no nonce/pty_session_id

	res := waitForResult(t, frames, "d1")
	if res.Status != job.StatusFailed {
		t.Fatalf("result status = %q, want failed", res.Status)
	}
	if !strings.Contains(res.Error, "missing relay credentials") {
		t.Fatalf("result error = %q, want to mention missing relay credentials", res.Error)
	}
	jobs.mu.Lock()
	calls := jobs.submitCalls
	jobs.mu.Unlock()
	if calls != 0 {
		t.Fatalf("Submit called %d times on fail-fast, want 0", calls)
	}
}

// TestHandleDispatchInteractiveProjection (T5): a credentialed interactive dispatch
// projects Interactive/Cols/Rows onto the worker's local Submit (so its job.Service
// picks the pty runner). No session is delivered here, so waitSession returns nil
// and no pump is started — the assertion is purely the Submit projection.
func TestHandleDispatchInteractiveProjection(t *testing.T) {
	jobs := &stubJobs{
		submitID:   "local-1",
		waitResult: job.JobResult{Status: job.StatusDone, ExitCode: 0},
		waitOK:     true,
	}
	cl, frames, url := dialLiveClient(t, jobs)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cl.handleDispatch(ctx, url, wsproto.Dispatch{
		JobID: "d1", Interactive: true, Cols: 120, Rows: 40,
		RelayNonce: "nonce-1", PtySessionID: "psid-1",
	})

	res := waitForResult(t, frames, "d1")
	if res.Status != job.StatusDone {
		t.Fatalf("result status = %q, want done", res.Status)
	}
	jobs.mu.Lock()
	gotI, gotC, gotR := jobs.gotInteractive, jobs.gotCols, jobs.gotRows
	jobs.mu.Unlock()
	if !gotI || gotC != 120 || gotR != 40 {
		t.Fatalf("Submit projection = interactive:%v cols:%d rows:%d, want true/120/40", gotI, gotC, gotR)
	}
}

// TestHandleDispatchPendingCancel (T5, D-P2-9): a cancel that arrived before the
// mapping existed is consumed after putJobMapping and cancels the fresh local job
// (covers a non-interactive dispatch).
func TestHandleDispatchPendingCancel(t *testing.T) {
	jobs := &stubJobs{
		submitID:   "local-1",
		waitResult: job.JobResult{Status: job.StatusCancelled, ExitCode: -1},
		waitOK:     true,
	}
	cl, frames, url := dialLiveClient(t, jobs)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cl.recordPendingCancel("d1") // cancel raced ahead of the mapping
	cl.handleDispatch(ctx, url, wsproto.Dispatch{JobID: "d1", ProjectKey: "alpha", Agent: "exec", Cmd: []string{"sleep", "1"}})

	_ = waitForResult(t, frames, "d1")
	if got := jobs.cancelled(); got != "local-1" {
		t.Fatalf("cancelled local id = %q, want local-1", got)
	}
}

// TestHandleDispatchJoinsPumpBeforeResult (T5): the terminal Result is not sent
// until the interactive pump's pumpDone closes (worker drained + closed the pty ws
// → serve recordLoop EOF, so the browser sees tail bytes before the job finishes).
func TestHandleDispatchJoinsPumpBeforeResult(t *testing.T) {
	jobs := &stubJobs{
		submitID:   "local-1",
		waitResult: job.JobResult{Status: job.StatusDone, ExitCode: 0},
		waitOK:     true,
	}
	cl, frames, url := dialLiveClient(t, jobs)
	// Override the pump with a controllable blocking pumpDone (no real 2nd ws/pty).
	pumpBlock := make(chan struct{})
	var pumpCalls int32
	cl.pumpPtyFn = func(_ context.Context, _, _, _, _, _ string, _ ptySession) <-chan struct{} {
		atomic.AddInt32(&pumpCalls, 1)
		return pumpBlock // stays open until the test closes it
	}
	// Buffer the session so waitSession returns it immediately (localID = submitID).
	cl.OnSessionStart("local-1", &ptyrunner.PtySession{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go cl.handleDispatch(ctx, url, wsproto.Dispatch{
		JobID: "d1", Interactive: true, RelayNonce: "nonce-1", PtySessionID: "psid-1",
	})

	// While pumpDone is open the Result must NOT arrive (any incidental non-Result
	// frame is fine; only a Result is a violation).
	held := false
	deadline := time.After(300 * time.Millisecond)
	for !held {
		select {
		case env := <-frames:
			if env.Type == wsproto.TypeResult {
				t.Fatalf("Result sent before pump join (pumpDone still open): %+v", env)
			}
		case <-deadline:
			held = true
		}
	}
	if atomic.LoadInt32(&pumpCalls) != 1 {
		t.Fatalf("pump launched %d times, want 1", atomic.LoadInt32(&pumpCalls))
	}

	close(pumpBlock) // pump done → join releases → Result sent
	res := waitForResult(t, frames, "d1")
	if res.Status != job.StatusDone {
		t.Fatalf("result status = %q, want done", res.Status)
	}
}

// waitForResult waits for a result frame for jobID on frames.
func waitForResult(t *testing.T, frames chan wsproto.Envelope, jobID string) wsproto.Result {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case env := <-frames:
			if env.Type == wsproto.TypeResult && env.JobID == jobID {
				r, _ := wsproto.As[wsproto.Result](env)
				return r
			}
		case <-deadline:
			t.Fatal("did not receive result frame")
		}
	}
}
