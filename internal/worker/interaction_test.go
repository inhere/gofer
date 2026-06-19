package worker

import (
	"context"
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

// fakeHubConn is a server-side WS conn handle a test uses to read frames the
// worker pushes and to send cancel/answer frames down to the worker.
type fakeHubConn struct {
	mu     sync.Mutex
	conn   *websocket.Conn
	frames chan wsproto.Envelope
	ready  chan struct{} // closed once conn is set + dispatch sent
}

// setConn stores the server conn under the lock (written by the accept handler).
func (f *fakeHubConn) setConn(c *websocket.Conn) {
	f.mu.Lock()
	f.conn = c
	f.mu.Unlock()
}

// getConn returns the server conn under the lock (read by the test send path).
func (f *fakeHubConn) getConn() *websocket.Conn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conn
}

// send writes a typed envelope to the worker (server→worker direction). It first
// waits for ready so conn is guaranteed set + the dispatch already delivered.
func (f *fakeHubConn) send(t *testing.T, typ wsproto.FrameType, jobID string, payload any) {
	t.Helper()
	<-f.ready
	if err := wsjson.Write(context.Background(), f.getConn(), wsproto.Envelope{
		Type: typ, JobID: jobID, Payload: mustRaw(payload),
	}); err != nil {
		t.Fatalf("send %s: %v", typ, err)
	}
}

// connectFlexHub stands up a fake hub that accepts the WS, reads the register,
// acks it, dispatches d1, and then relays every inbound worker frame onto a
// channel while exposing the server conn so the test can push frames down. It
// does NOT auto-finish; the test drives the flow.
func connectFlexHub(t *testing.T, jobs Jobs) (*Client, *fakeHubConn) {
	t.Helper()
	hc := &fakeHubConn{frames: make(chan wsproto.Envelope, 64), ready: make(chan struct{})}
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		hc.setConn(conn)
		ctx := req.Context()
		var reg wsproto.Envelope
		if err := wsjson.Read(ctx, conn, &reg); err != nil {
			return
		}
		_ = wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeRegistered, Payload: mustRaw(wsproto.Registered{Accepted: true})})
		_ = wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeDispatch, JobID: "d1", Payload: mustRaw(wsproto.Dispatch{
			JobID: "d1", ProjectKey: "alpha", Agent: "exec", Runner: "local", Cmd: []string{"echo", "hi"},
		})})
		close(hc.ready)
		for {
			var env wsproto.Envelope
			if err := wsjson.Read(ctx, conn, &env); err != nil {
				return
			}
			hc.frames <- env
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workers/connect"
	cl := New(Config{WorkerID: "w1", URL: wsURL, Token: "t"}, jobs)
	cl.pollInterval = 20 * time.Millisecond
	return cl, hc
}

// waitForFrame waits for the next frame of typ for jobID.
func waitForFrame(t *testing.T, hc *fakeHubConn, typ wsproto.FrameType, jobID string) wsproto.Envelope {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case env := <-hc.frames:
			if env.Type == typ && env.JobID == jobID {
				return env
			}
		case <-deadline:
			t.Fatalf("did not receive %s frame for %s", typ, jobID)
		}
	}
}

// TestWorkerPushesInteractionOpen: a local job with a pending interaction is
// observed by streamLocalJob and pushed up as interaction{open}.
func TestWorkerPushesInteractionOpen(t *testing.T) {
	jobs := &stubJobs{
		submitID:   "local-1",
		submitDir:  t.TempDir() + "/alpha/local-1",
		getResult:  job.JobResult{Status: job.StatusPendingInteraction},
		getOK:      true,
		waitResult: job.JobResult{Status: job.StatusDone},
		waitOK:     true,
		interactions: []job.Interaction{
			{ID: "i1", Type: "question", Prompt: "need input", Status: job.InteractionPending},
		},
	}
	cl, hc := connectFlexHub(t, jobs)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = cl.Run(ctx) }()

	env := waitForFrame(t, hc, wsproto.TypeInteraction, "d1")
	ifr, _ := wsproto.As[wsproto.Interaction](env)
	if ifr.Action != "open" {
		t.Fatalf("action = %q, want open", ifr.Action)
	}
	if !strings.Contains(string(ifr.Interaction), `"i1"`) {
		t.Fatalf("interaction body missing id i1: %s", ifr.Interaction)
	}
}

// TestWorkerReceivesAnswer: an inbound answer frame is delivered to the local
// job via AnswerInteraction (keyed by the local job id resolved from the hub id).
func TestWorkerReceivesAnswer(t *testing.T) {
	jobs := &stubJobs{
		submitID:   "local-1",
		submitDir:  t.TempDir() + "/alpha/local-1",
		getResult:  job.JobResult{Status: job.StatusRunning},
		getOK:      true,
		waitResult: job.JobResult{Status: job.StatusDone},
		waitOK:     true,
	}
	cl, hc := connectFlexHub(t, jobs)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = cl.Run(ctx) }()

	// Wait until the dispatch is mapped (the worker has submitted the local job).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && cl.localJobID("d1") == "" {
		time.Sleep(10 * time.Millisecond)
	}
	hc.send(t, wsproto.TypeAnswer, "d1", wsproto.Answer{JobID: "d1", InteractionID: "i1", Answer: "yes"})

	for time.Now().Before(deadline) {
		jobID, iid, ans := jobs.answered()
		if iid == "i1" {
			if jobID != "local-1" || ans != "yes" {
				t.Fatalf("AnswerInteraction(%q,%q,%q), want (local-1,i1,yes)", jobID, iid, ans)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("AnswerInteraction was never called")
}

// TestWorkerReceivesCancel: an inbound cancel frame cancels the matching local
// job; the worker then pushes a result.
func TestWorkerReceivesCancel(t *testing.T) {
	jobs := &stubJobs{
		submitID:   "local-1",
		submitDir:  t.TempDir() + "/alpha/local-1",
		getResult:  job.JobResult{Status: job.StatusRunning},
		getOK:      true,
		waitResult: job.JobResult{Status: job.StatusCancelled, ExitCode: -1},
		waitOK:     true,
	}
	cl, hc := connectFlexHub(t, jobs)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = cl.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && cl.localJobID("d1") == "" {
		time.Sleep(10 * time.Millisecond)
	}
	hc.send(t, wsproto.TypeCancel, "d1", wsproto.Cancel{JobID: "d1"})

	for time.Now().Before(deadline) {
		if jobs.cancelled() == "local-1" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Cancel(local-1) was never called")
}
