package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/job"
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
	submitCalls int

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
	// Use a stdlib handler (not rux): the stdlib ResponseWriter implements
	// http.Hijacker/Flusher directly without rux's deferred-WriteHeader buffering,
	// so Accept upgrades cleanly. (The rux-specific wsUpgradeWriter path is
	// covered by wshub's own tests + the e2e.)
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
	cl := New(Config{WorkerID: "w1", URL: wsURL, Token: "t"}, jobs)
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
	// A hub that rejects the register → Run returns an error.
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		ctx := req.Context()
		var reg wsproto.Envelope
		_ = wsjson.Read(ctx, conn, &reg)
		_ = wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeRegistered, Payload: mustRaw(wsproto.Registered{Accepted: false, Reason: "nope"})})
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workers/connect"
	cl := New(Config{WorkerID: "w1", URL: wsURL, Token: "t"}, &stubJobs{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := cl.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "register rejected") {
		t.Fatalf("expected register rejected error, got %v", err)
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
