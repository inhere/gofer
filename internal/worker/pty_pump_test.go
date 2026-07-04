package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/job"
	ptyrunner "github.com/inhere/gofer/internal/runner/pty"
)

// --- fakes ------------------------------------------------------------------

// pumpJobs is a minimal worker.Jobs that only records Cancel calls (the pump
// touches nothing else). Submit/Get/Wait are unused by pumpPty.
type pumpJobs struct {
	mu        sync.Mutex
	cancelled []string
}

func (p *pumpJobs) Submit(job.JobRequest) (job.JobResult, error) { return job.JobResult{}, nil }
func (p *pumpJobs) Get(string) (job.JobResult, bool)             { return job.JobResult{}, false }
func (p *pumpJobs) Wait(string) (job.JobResult, bool)            { return job.JobResult{}, false }
func (p *pumpJobs) Cancel(id string) error {
	p.mu.Lock()
	p.cancelled = append(p.cancelled, id)
	p.mu.Unlock()
	return nil
}
func (p *pumpJobs) GetInteractions(string) ([]job.Interaction, error) { return nil, nil }
func (p *pumpJobs) AnswerInteraction(string, string, string) (job.Interaction, error) {
	return job.Interaction{}, nil
}
func (p *pumpJobs) cancelledIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.cancelled...)
}

// fakeSession stands in for *ptyrunner.PtySession (narrow ptySession interface):
// Read blocks until a chunk is fed (or reads is closed → EOF), and WriteInput /
// Resize are captured for assertion. state is switchable so tests can drive the
// disconnect-judgement branch.
type fakeSession struct {
	reads   chan []byte // feed output chunks; close ⇒ Read returns EOF (teardown)
	readErr error       // returned when reads is closed (default io.EOF)

	mu      sync.Mutex
	written [][]byte
	resizes [][2]int
	state   string
}

func newFakeSession(state string) *fakeSession {
	return &fakeSession{reads: make(chan []byte, 8), state: state}
}

func (f *fakeSession) Read(p []byte) (int, error) {
	chunk, ok := <-f.reads
	if !ok {
		if f.readErr != nil {
			return 0, f.readErr
		}
		return 0, io.EOF
	}
	return copy(p, chunk), nil
}

func (f *fakeSession) WriteInput(b []byte) (int, error) {
	f.mu.Lock()
	f.written = append(f.written, append([]byte(nil), b...))
	f.mu.Unlock()
	return len(b), nil
}

func (f *fakeSession) Resize(cols, rows int) error {
	f.mu.Lock()
	f.resizes = append(f.resizes, [2]int{cols, rows})
	f.mu.Unlock()
	return nil
}

func (f *fakeSession) State() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *fakeSession) writes() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.written...)
}

func (f *fakeSession) resizeList() [][2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]int(nil), f.resizes...)
}

// startFakePtyServe stands up an httptest server whose ws upgrade reads the pty
// hello then hands (conn, hello) to drive (which blocks for the connection's life,
// keeping the hijacked conn alive). It returns a hub-style session URL ending in
// /v1/workers/connect so derivePtyConnectURL swaps it to /v1/workers/pty-connect.
func startFakePtyServe(t *testing.T, drive func(ctx context.Context, conn *websocket.Conn, hello ptyConnectHello)) string {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{
			InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			return
		}
		var hello ptyConnectHello
		if err := wsjson.Read(req.Context(), conn, &hello); err != nil {
			_ = conn.Close(websocket.StatusProtocolError, "bad hello")
			return
		}
		drive(req.Context(), conn, hello)
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workers/connect"
}

func newPumpClient(jobs Jobs) *Client {
	return New(Config{WorkerID: "w1", URLs: []string{"ws://x/y"}, Token: "tok"}, jobs)
}

// eventuallyTrue polls cond until it holds or the deadline lapses.
func eventuallyTrue(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("condition never held: %s", msg)
		case <-time.After(time.Millisecond):
		}
	}
}

func sliceHas(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// --- pure-function unit tests ----------------------------------------------

func TestDerivePtyConnectURL(t *testing.T) {
	ok := []struct{ in, want string }{
		{"ws://h:9090/v1/workers/connect", "ws://h:9090/v1/workers/pty-connect"},
		{"wss://host/v1/workers/connect", "wss://host/v1/workers/pty-connect"},
		{"ws://h/v1/workers/connect?a=b", "ws://h/v1/workers/pty-connect"}, // query stripped
	}
	for _, c := range ok {
		got, err := derivePtyConnectURL(c.in)
		if err != nil {
			t.Fatalf("derivePtyConnectURL(%q) err = %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("derivePtyConnectURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "/only/path", "://no-scheme"} {
		if _, err := derivePtyConnectURL(bad); err == nil {
			t.Fatalf("derivePtyConnectURL(%q) err = nil, want fail-fast", bad)
		}
	}
}

func TestClampSize(t *testing.T) {
	cases := []struct{ cols, rows, wc, wr int }{
		{80, 24, 80, 24},
		{0, 0, 1, 1},
		{-5, -9, 1, 1},
		{999, 999, 500, 200},
		{500, 200, 500, 200},
	}
	for _, c := range cases {
		gc, gr := clampSize(c.cols, c.rows)
		if gc != c.wc || gr != c.wr {
			t.Fatalf("clampSize(%d,%d) = (%d,%d), want (%d,%d)", c.cols, c.rows, gc, gr, c.wc, c.wr)
		}
	}
}

// --- pump integration tests -------------------------------------------------

func TestPumpPtyHelloAndOutput(t *testing.T) {
	helloCh := make(chan ptyConnectHello, 1)
	gotBin := make(chan []byte, 4)
	hubURL := startFakePtyServe(t, func(ctx context.Context, conn *websocket.Conn, hello ptyConnectHello) {
		helloCh <- hello
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				gotBin <- append([]byte(nil), data...)
			}
		}
	})

	jobs := &pumpJobs{}
	cl := newPumpClient(jobs)
	sess := newFakeSession(ptyrunner.StateRunning)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := cl.pumpPty(ctx, hubURL, "local-1", "remote-9", "psid-1", "nonce-1", sess)

	select {
	case h := <-helloCh:
		if h.JobID != "remote-9" || h.PtySessionID != "psid-1" || h.RelayNonce != "nonce-1" {
			t.Fatalf("hello = %+v", h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hello never arrived at serve")
	}

	sess.reads <- []byte("out-bytes")
	select {
	case b := <-gotBin:
		if string(b) != "out-bytes" {
			t.Fatalf("serve binary = %q, want out-bytes", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve never received pty output binary")
	}

	// sess EOF ends the pump cleanly (selfClosing) → no cancel.
	close(sess.reads)
	<-done
	if ids := jobs.cancelledIDs(); len(ids) != 0 {
		t.Fatalf("unexpected cancel on clean output flow: %v", ids)
	}
}

func TestPumpPtyInput(t *testing.T) {
	hubURL := startFakePtyServe(t, func(ctx context.Context, conn *websocket.Conn, _ ptyConnectHello) {
		_ = conn.Write(ctx, websocket.MessageBinary, []byte("keystroke"))
		msg, _ := json.Marshal(ptyResizeMsg{Type: "resize", Cols: 999, Rows: -5})
		_ = conn.Write(ctx, websocket.MessageText, msg)
		// then read until the worker closes (selfClosing path) so no external drop.
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	})

	jobs := &pumpJobs{}
	cl := newPumpClient(jobs)
	sess := newFakeSession(ptyrunner.StateRunning)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := cl.pumpPty(ctx, hubURL, "local-1", "remote-9", "psid-1", "nonce-1", sess)

	eventuallyTrue(t, func() bool { return len(sess.writes()) == 1 }, "input byte forwarded to sess")
	if got := sess.writes()[0]; string(got) != "keystroke" {
		t.Fatalf("WriteInput got %q, want keystroke", got)
	}
	eventuallyTrue(t, func() bool { return len(sess.resizeList()) == 1 }, "resize forwarded to sess")
	if got := sess.resizeList()[0]; got != [2]int{500, 1} { // 999→500, -5→1
		t.Fatalf("Resize got %v, want [500 1] (clamped)", got)
	}

	// End via sess EOF so the input path is not conflated with a cancel.
	close(sess.reads)
	<-done
	if ids := jobs.cancelledIDs(); len(ids) != 0 {
		t.Fatalf("unexpected cancel on input flow: %v", ids)
	}
}

func TestPumpPtySelfClosingNoCancel(t *testing.T) {
	serveClosed := make(chan error, 1)
	hubURL := startFakePtyServe(t, func(ctx context.Context, conn *websocket.Conn, _ ptyConnectHello) {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				serveClosed <- err
				return
			}
		}
	})

	jobs := &pumpJobs{}
	cl := newPumpClient(jobs)
	sess := newFakeSession(ptyrunner.StateRunning)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := cl.pumpPty(ctx, hubURL, "local-1", "remote-9", "psid-1", "nonce-1", sess)

	// sess EOF → out actively closes the ws (serve recordLoop would see EOF), no cancel.
	close(sess.reads)
	<-done
	if ids := jobs.cancelledIDs(); len(ids) != 0 {
		t.Fatalf("self-close must NOT cancel, got %v", ids)
	}
	select {
	case err := <-serveClosed:
		if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
			t.Fatalf("serve close status = %v, want normal closure", websocket.CloseStatus(err))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve never observed the pty ws close")
	}
}

func TestPumpPtyExternalDisconnectCancelsRunning(t *testing.T) {
	hubURL := startFakePtyServe(t, func(_ context.Context, conn *websocket.Conn, _ ptyConnectHello) {
		_ = conn.Close(websocket.StatusNormalClosure, "external drop")
	})

	jobs := &pumpJobs{}
	cl := newPumpClient(jobs)
	sess := newFakeSession(ptyrunner.StateRunning)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := cl.pumpPty(ctx, hubURL, "local-1", "remote-9", "psid-1", "nonce-1", sess)

	// ws dropped under a running session → local job must be cancelled.
	eventuallyTrue(t, func() bool { return sliceHas(jobs.cancelledIDs(), "local-1") },
		"external disconnect cancels the running local job")

	close(sess.reads) // unblock out so the pump can finish
	<-done
}

func TestPumpPtyExternalDisconnectCancelsStarting(t *testing.T) {
	// D-P2-5: a drop while the session is still STARTING (sess.run not yet running)
	// must also cancel — the starting window is not exempt.
	hubURL := startFakePtyServe(t, func(_ context.Context, conn *websocket.Conn, _ ptyConnectHello) {
		_ = conn.Close(websocket.StatusNormalClosure, "external drop")
	})

	jobs := &pumpJobs{}
	cl := newPumpClient(jobs)
	sess := newFakeSession(ptyrunner.StateStarting)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := cl.pumpPty(ctx, hubURL, "local-1", "remote-9", "psid-1", "nonce-1", sess)

	eventuallyTrue(t, func() bool { return sliceHas(jobs.cancelledIDs(), "local-1") },
		"disconnect during starting window cancels the local job")

	close(sess.reads)
	<-done
}

func TestPumpPtyBadURLCancelsBeforeDial(t *testing.T) {
	jobs := &pumpJobs{}
	cl := newPumpClient(jobs)
	sess := newFakeSession(ptyrunner.StateRunning)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Malformed hub URL → derivePtyConnectURL fails → cancel without dialing.
	done := cl.pumpPty(ctx, "://bad-url", "local-1", "remote-9", "psid-1", "nonce-1", sess)
	<-done
	if !sliceHas(jobs.cancelledIDs(), "local-1") {
		t.Fatalf("bad url must cancel, got %v", jobs.cancelledIDs())
	}
}

func TestPumpPtyDialFailureCancels(t *testing.T) {
	jobs := &pumpJobs{}
	cl := newPumpClient(jobs)
	sess := newFakeSession(ptyrunner.StateRunning)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Nothing is listening on 127.0.0.1:1 → dial fails → cancel.
	done := cl.pumpPty(ctx, "ws://127.0.0.1:1/v1/workers/connect", "local-1", "remote-9", "psid-1", "nonce-1", sess)
	<-done
	if !sliceHas(jobs.cancelledIDs(), "local-1") {
		t.Fatalf("dial failure must cancel, got %v", jobs.cancelledIDs())
	}
}
