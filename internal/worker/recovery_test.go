package worker

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/wsproto"
)

// This file is the WORKER half of RECOV-01 (worker-disconnect recovery): the hub
// may hold a job in `recovering` while this process keeps running it, so the worker
// must never lose a log byte or a terminal Result to a connection blip.
//
// The server half lives in internal/wshub/recovery.go (+ the runner sink); these
// tests deliberately do NOT re-test it. recoverHub below is the smallest hub that
// can exercise the worker's side of the contract: it performs the handshake, records
// every register frame and every frame pushed afterwards, and lets a test drive a
// blip (drop the live connection, optionally hold the NEXT accept off-line).

// recoverHub is the hub-side test double for the RECOV-01 worker tests.
type recoverHub struct {
	srv *httptest.Server

	mu    sync.Mutex
	regs  []wsproto.Register
	envs  []wsproto.Envelope
	conn  *websocket.Conn
	conns int
	// gate, when non-nil, is consumed by every accept AFTER the first: the test
	// releases one token to let a reconnect complete its handshake. Up to the
	// register frame everything has already happened, but no ack was sent — so the
	// worker is still attached to its previous (dead) connection, which is exactly
	// the window the recovery tests need to control.
	gate chan struct{}
	// resumeFn supplies the resume list of each ack (nil → no resume entries), i.e.
	// what the real hub would rewind the worker to.
	resumeFn func(reg wsproto.Register) []wsproto.ResumeJob
}

func newRecoverHub(t *testing.T) *recoverHub {
	t.Helper()
	h := &recoverHub{}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *recoverHub) wsURL() string {
	return "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/v1/workers/connect"
}

func (h *recoverHub) serve(w http.ResponseWriter, req *http.Request) {
	conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{
		InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "bye")
	ctx := req.Context()
	var env wsproto.Envelope
	if err := wsjson.Read(ctx, conn, &env); err != nil {
		return
	}
	reg, _ := wsproto.As[wsproto.Register](env)

	h.mu.Lock()
	h.conns++
	h.conn = conn
	h.regs = append(h.regs, reg)
	gate, resumeFn, conns := h.gate, h.resumeFn, h.conns
	h.mu.Unlock()

	if gate != nil && conns > 1 {
		select {
		case <-gate:
		case <-ctx.Done():
			return
		}
	}
	var resume []wsproto.ResumeJob
	if resumeFn != nil {
		resume = resumeFn(reg)
	}
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type:    wsproto.TypeRegistered,
		Payload: mustRaw(wsproto.Registered{Accepted: true, Resume: resume}),
	}); err != nil {
		return
	}
	for {
		var f wsproto.Envelope
		if err := wsjson.Read(ctx, conn, &f); err != nil {
			return
		}
		h.mu.Lock()
		h.envs = append(h.envs, f)
		h.mu.Unlock()
	}
}

// dropConn closes the live connection from the hub side (a blip): the worker sees
// the going-away close, its recv loop returns, and everything in flight must
// survive it. The hub keeps accepting.
func (h *recoverHub) dropConn() {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn != nil {
		_ = conn.Close(websocket.StatusGoingAway, "hub blip")
	}
}

func (h *recoverHub) setGate(g chan struct{}) {
	h.mu.Lock()
	h.gate = g
	h.mu.Unlock()
}

func (h *recoverHub) setResume(fn func(reg wsproto.Register) []wsproto.ResumeJob) {
	h.mu.Lock()
	h.resumeFn = fn
	h.mu.Unlock()
}

func (h *recoverHub) connectionCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns
}

func (h *recoverHub) registerFrames() []wsproto.Register {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]wsproto.Register(nil), h.regs...)
}

func (h *recoverHub) frames() []wsproto.Envelope {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]wsproto.Envelope(nil), h.envs...)
}

func (h *recoverHub) waitConnections(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	waitForCond(t, "hub connection count", timeout, func() bool { return h.connectionCount() >= n })
}

func (h *recoverHub) waitRegisters(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	waitForCond(t, "register frame", timeout, func() bool { return len(h.registerFrames()) >= n })
}

// waitResult waits for a result frame for jobID and returns it decoded.
func (h *recoverHub) waitResult(t *testing.T, jobID string, timeout time.Duration) wsproto.Result {
	t.Helper()
	var out wsproto.Result
	waitForCond(t, "result frame for "+jobID, timeout, func() bool {
		for _, env := range h.frames() {
			if env.Type == wsproto.TypeResult && env.JobID == jobID {
				out, _ = wsproto.As[wsproto.Result](env)
				return true
			}
		}
		return false
	})
	return out
}

func waitForCond(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// captureWorkerLogs redirects the default slog handler to a synchronized buffer for
// the duration of the test so it can assert the RECOV-01 events were emitted. The
// reconnect loop logs from its own goroutines, hence the lock around the buffer.
func captureWorkerLogs(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logSyncWriter{mu: &mu, w: &buf}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

type logSyncWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (s *logSyncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// dialRecordConn stands up a bare ws server that records every frame the worker
// writes to it, and returns a dialed client connection (plus a stop func) that a
// test can install via cl.setConn without running the reconnect supervisor.
func dialRecordConn(t *testing.T, frames chan wsproto.Envelope) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{
			InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "bye")
		for {
			var env wsproto.Envelope
			if err := wsjson.Read(req.Context(), conn, &env); err != nil {
				return
			}
			select {
			case frames <- env:
			default:
			}
		}
	}))
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workers/connect"
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatalf("dial recording hub: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })
	return conn
}

// runningJobs is a Jobs stub whose single local job stays RUNNING until the test
// finishes it, so a stream/dispatch can be driven across a blip deterministically.
type runningJobs struct {
	mu     sync.Mutex
	status string
	done   chan struct{}
	final  job.JobResult
	dir    string
}

func newRunningJobs(t *testing.T) *runningJobs {
	t.Helper()
	return &runningJobs{status: job.StatusRunning, done: make(chan struct{}), dir: filepath.Join(t.TempDir(), "results")}
}

func (j *runningJobs) Submit(job.JobRequest) (job.JobResult, error) {
	return job.JobResult{ID: "local-1", ResultDir: j.dir, Status: job.StatusRunning}, nil
}

func (j *runningJobs) Get(string) (job.JobResult, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return job.JobResult{ID: "local-1", Status: j.status}, true
}

func (j *runningJobs) Wait(string) (job.JobResult, bool) {
	<-j.done
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.final, true
}

func (j *runningJobs) Cancel(string) error                               { return nil }
func (j *runningJobs) GetInteractions(string) ([]job.Interaction, error) { return nil, nil }
func (j *runningJobs) AnswerInteraction(string, string, string) (job.Interaction, error) {
	return job.Interaction{}, nil
}

// SetEventObserver accepts (and ignores) the SUP-01 G mirror hook: this fake raises
// no job events.
func (j *runningJobs) SetEventObserver(job.JobEventObserver) {}

// Config is unused by the recovery unit tests.
func (j *runningJobs) Config() *config.Config { return nil }

// finish drives the local job terminal and releases Wait.
func (j *runningJobs) finish(res job.JobResult) {
	j.mu.Lock()
	had := j.status
	j.status = res.Status
	j.final = res
	j.mu.Unlock()
	if !job.IsTerminal(had) {
		close(j.done)
	}
}

// TestStreamDoesNotAdvanceOffsetOnWriteFailure is acceptance "offset advances only
// on a successful write" (RECOV-01 #1): a failed writeFrame leaves the stored stream
// offset AND the seq exactly where they were — that is what lets the same bytes be
// re-sent after the reconnect instead of being skipped — while a later successful
// write advances both.
//
// Falsification: restore `*ent.off = next; seq++` BEFORE the writeFrame call (the
// pre-RECOV-01 code). The first assertion below then fails on an offset of 6 for a
// frame that was never written.
func TestStreamDoesNotAdvanceOffsetOnWriteFailure(t *testing.T) {
	jobs := newRunningJobs(t)
	tmp := t.TempDir()
	localID := "local-1"
	// streamLocalJob derives the log dir from the result dir: base = Dir(resultDir),
	// logs live in <base>/<localID>/.
	resultDir := filepath.Join(tmp, "result")
	logDir := filepath.Join(tmp, localID)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	content := "hello\n"
	if err := os.WriteFile(filepath.Join(logDir, store.StdoutFile), []byte(content), 0o600); err != nil {
		t.Fatalf("write stdout log: %v", err)
	}

	cl := New(Config{WorkerID: "w1", Token: "t"}, jobs)
	cl.pollInterval = 10 * time.Millisecond
	cl.inflightCreate("job-1")
	cl.inflightSetLocal("job-1", localID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		cl.streamLocalJob(ctx, localID, resultDir, "job-1")
	}()

	// (a) no connection at all: several poll ticks' worth of writes fail.
	time.Sleep(100 * time.Millisecond)
	if got := cl.inflightOffset("job-1", string(store.StreamStdout)); got != 0 {
		t.Fatalf("stdout offset = %d after failed writes, want 0", got)
	}
	if got := cl.inflightSeq("job-1"); got != 0 {
		t.Fatalf("seq = %d after failed writes, want 0", got)
	}

	// (b) a connection that has already died — the blip case: writes still fail, and
	// still nothing may advance.
	dead := dialRecordConn(t, make(chan wsproto.Envelope, 1))
	_ = dead.Close(websocket.StatusNormalClosure, "killed")
	cl.setConn(dead)
	time.Sleep(60 * time.Millisecond)
	if got := cl.inflightOffset("job-1", string(store.StreamStdout)); got != 0 {
		t.Fatalf("stdout offset = %d after a write to a dead connection, want 0", got)
	}
	if got := cl.inflightSeq("job-1"); got != 0 {
		t.Fatalf("seq = %d after a write to a dead connection, want 0", got)
	}

	// (c) a live connection: the withheld chunk goes out and the offsets follow the
	// successful write — exactly once, so a blip neither drops nor duplicates it.
	frames := make(chan wsproto.Envelope, 8)
	cl.setConn(dialRecordConn(t, frames))
	waitForCond(t, "the withheld log chunk to be sent", 3*time.Second, func() bool {
		return cl.inflightOffset("job-1", string(store.StreamStdout)) == int64(len(content))
	})
	if got := cl.inflightSeq("job-1"); got != 1 {
		t.Fatalf("seq = %d after one successful write, want 1", got)
	}
	// Exactly one log frame: the bytes suppressed while disconnected were not also
	// sent before/after.
	sent := 0
	deadline := time.After(200 * time.Millisecond)
collect:
	for {
		select {
		case env := <-frames:
			if env.Type == wsproto.TypeLog {
				sent++
			}
		case <-deadline:
			break collect
		}
	}
	if sent != 1 {
		t.Fatalf("log frames sent = %d, want exactly 1", sent)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("streamLocalJob did not return after ctx cancel")
	}
}

// TestRegisterCarriesInflight is acceptance #3: every register frame reports this
// process's in-flight table — including the load-bearing EMPTY, non-nil slice when
// nothing is in flight (nil would tell the hub "pre-RECOV-01 worker, it cannot prove
// anything" and make it wait out its whole window for jobs it should fail at once).
//
// Falsification: drop the `Inflight:` field from the register payload → the first
// assertion fails (nil). Send `var out []wsproto.InflightJob` instead of a
// make(...) → the empty-but-non-nil assertion fails.
func TestRegisterCarriesInflight(t *testing.T) {
	jobs := newRunningJobs(t)
	hub := newRecoverHub(t)
	cl := New(Config{
		WorkerID: "w1", Token: "t", URLs: []string{hub.wsURL()},
		InitialBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond,
	}, jobs)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() { _ = cl.Run(ctx) }()
	hub.waitRegisters(t, 1, 5*time.Second)

	first := hub.registerFrames()[0]
	if first.Inflight == nil {
		t.Fatal("first register carried a nil inflight slice: the hub would treat this worker as pre-RECOV-01")
	}
	if len(first.Inflight) != 0 {
		t.Fatalf("first register inflight = %+v, want empty", first.Inflight)
	}

	// A dispatch is in flight, with log bytes already on the wire.
	cl.inflightCreate("job-a")
	cl.inflightSetLocal("job-a", "local-a")
	cl.inflightSetStatus("job-a", job.StatusRunning)
	cl.inflightCommit("job-a", string(store.StreamStdout), 6, 1)
	cl.inflightCommit("job-a", string(store.StreamStderr), 3, 2)

	// Blip: the hub closes the connection; the worker re-registers and must report
	// what it still holds, offsets included.
	hub.dropConn()
	hub.waitRegisters(t, 2, 5*time.Second)

	got := hub.registerFrames()[1].Inflight
	if len(got) != 1 {
		t.Fatalf("second register inflight = %+v, want exactly the one in-flight job", got)
	}
	if got[0].JobID != "job-a" || got[0].Status != job.StatusRunning {
		t.Fatalf("inflight entry = %+v, want job-a/%s", got[0], job.StatusRunning)
	}
	if got[0].StdoutOff != 6 || got[0].StderrOff != 3 || got[0].Seq != 2 {
		t.Fatalf("inflight offsets = %+v, want stdout 6 / stderr 3 / seq 2", got[0])
	}
}

// TestResumeRewindsOffsets is acceptance #4: the ack's resume entry moves the
// in-flight entry's offsets to the hub's durable byte counts BEFORE streaming
// resumes, and the tailer reads them from that shared entry — so the very next log
// frame starts at the rewound offset (no gap, no duplicate) rather than at whatever
// the goroutine last read.
//
// Falsification: keep the offsets in streamLocalJob's stack (the pre-RECOV-01 code)
// → the rewind is invisible to the tailer and the frame below starts at "0123456789".
func TestResumeRewindsOffsets(t *testing.T) {
	logs := captureWorkerLogs(t)
	jobs := newRunningJobs(t)
	tmp := t.TempDir()
	localID := "local-1"
	resultDir := filepath.Join(tmp, "result")
	logDir := filepath.Join(tmp, localID)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	const content = "0123456789ABC"
	if err := os.WriteFile(filepath.Join(logDir, store.StdoutFile), []byte(content), 0o600); err != nil {
		t.Fatalf("write stdout log: %v", err)
	}

	cl := New(Config{WorkerID: "w1", Token: "t"}, jobs)
	cl.pollInterval = 10 * time.Millisecond
	cl.inflightCreate("job-r")
	cl.inflightSetLocal("job-r", localID)
	// As if the first 10 bytes had been sent before the connection dropped.
	cl.inflightCommit("job-r", string(store.StreamStdout), 10, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := make(chan wsproto.Envelope, 8)
	conn := dialRecordConn(t, frames)
	cl.setConn(conn) // a live connection is what streaming resumes onto

	// The hub's ack rewinds us to byte 4 of stdout (it persisted only that far).
	cl.applyResume(ctx, conn, []wsproto.ResumeJob{{JobID: "job-r", StdoutOff: 4, StderrOff: 0}})
	if got := cl.inflightOffset("job-r", string(store.StreamStdout)); got != 4 {
		t.Fatalf("stdout offset = %d after resume, want 4", got)
	}
	if got := cl.inflightSeq("job-r"); got != 1 {
		t.Fatalf("seq = %d after resume, want 1 (seq is not rewound)", got)
	}
	if !strings.Contains(logs(), "worker.job_resumed") {
		t.Fatalf("no worker.job_resumed event logged; got:\n%s", logs())
	}

	// Streaming now resumes from the rewound offset.
	go cl.streamLocalJob(ctx, localID, resultDir, "job-r")
	var log wsproto.Log
	waitForCond(t, "the resumed log frame", 3*time.Second, func() bool {
		for _, env := range drainFrames(frames) {
			if env.Type == wsproto.TypeLog && env.JobID == "job-r" {
				log, _ = wsproto.As[wsproto.Log](env)
				return true
			}
		}
		return false
	})
	if want := content[4:]; log.Text != want {
		t.Fatalf("first resumed log frame = %q, want %q (replay from the hub's offset)", log.Text, want)
	}
	if got := cl.inflightOffset("job-r", string(store.StreamStdout)); got != int64(len(content)) {
		t.Fatalf("stdout offset = %d after replay, want %d", got, len(content))
	}
	cancel()
}

// TestResultReplayedAfterReconnect is acceptance #5: a job that finished while the
// connection was down has its terminal Result cached (there was nowhere to write it)
// and replayed on the next registered connection, with the entry dropped once it
// lands — otherwise the host job stays `recovering` and then fails(worker_lost) even
// though the worker ran it to completion.
//
// Falsification: make sendResult discard the Result on a write error (the
// pre-RECOV-01 behaviour: `_ = cl.writeFrame(...)`) → no Result frame ever reaches
// the hub below.
func TestResultReplayedAfterReconnect(t *testing.T) {
	jobs := newRunningJobs(t)
	jobs.finish(job.JobResult{ID: "local-1", Status: job.StatusDone, ExitCode: 0})
	hub := newRecoverHub(t)
	cl := New(Config{
		WorkerID: "w1", Token: "t", URLs: []string{hub.wsURL()},
		InitialBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond,
	}, jobs)
	cl.pollInterval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	disconnected := make(chan struct{})
	var once sync.Once
	cl.onSession = func(event string) {
		if event == "disconnected" {
			once.Do(func() { close(disconnected) })
		}
	}
	go func() { _ = cl.Run(ctx) }()
	hub.waitConnections(t, 1, 5*time.Second)

	// Blip, and hold the reconnect at the gate: the worker stays off-line (its
	// cl.conn is the dead connection) while the job finishes.
	gate := make(chan struct{}, 1)
	hub.setGate(gate)
	hub.dropConn()
	select {
	case <-disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never observed the disconnect")
	}

	// The dispatch runs its full course against the dead connection.
	cl.handleDispatch(ctx, hub.wsURL(), wsproto.Dispatch{
		JobID: "job-x", ProjectKey: "alpha", Agent: "exec", Runner: builtinLocalRunner, Cmd: []string{"echo", "hi"},
	})
	if _, ok := cl.inflightResult("job-x"); !ok {
		t.Fatal("the terminal Result was NOT cached while the connection was down")
	}

	// The reconnect lands: the register carries the job (terminal) and the cached
	// Result is replayed on that same, freshly handshaken connection.
	gate <- struct{}{}
	res := hub.waitResult(t, "job-x", 5*time.Second)
	if res.Status != job.StatusDone || res.ExitCode != 0 {
		t.Fatalf("replayed result = %+v, want done/0", res)
	}
	waitForCond(t, "the in-flight entry to be dropped after the replay", 3*time.Second, func() bool {
		return len(cl.inflightIDs()) == 0
	})
}

// TestDispatchOutlivesConnection is acceptance #6: a dispatch is tied to the JOB,
// not to the connection — when its connection dies mid-job the goroutine keeps
// running, its log tailer keeps polling, and the job completes normally (Result on
// the connection that came back).
//
// Falsification: run handleDispatch under a per-connection ctx (or return from it on
// a write error) → the dispatch below returns at the blip instead of at the job's
// terminal state.
func TestDispatchOutlivesConnection(t *testing.T) {
	logs := captureWorkerLogs(t)
	jobs := newRunningJobs(t)
	hub := newRecoverHub(t)
	// The hub is holding a `recovering` job and rewinds it on the reconnect: the
	// worker must log the resume event for it.
	hub.setResume(func(reg wsproto.Register) []wsproto.ResumeJob {
		for _, f := range reg.Inflight {
			if f.JobID == "job-y" {
				return []wsproto.ResumeJob{{JobID: "job-y"}}
			}
		}
		return nil
	})
	cl := New(Config{
		WorkerID: "w1", Token: "t", URLs: []string{hub.wsURL()},
		InitialBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond,
	}, jobs)
	cl.pollInterval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() { _ = cl.Run(ctx) }()
	hub.waitConnections(t, 1, 5*time.Second)

	// Hold the next accept so the job's whole middle happens OFF-line.
	gate := make(chan struct{}, 1)
	hub.setGate(gate)

	dispatchDone := make(chan struct{})
	go func() {
		defer close(dispatchDone)
		cl.handleDispatch(ctx, hub.wsURL(), wsproto.Dispatch{
			JobID: "job-y", ProjectKey: "alpha", Agent: "exec", Runner: builtinLocalRunner, Cmd: []string{"echo", "hi"},
		})
	}()
	waitForCond(t, "the dispatch to map its local job", 5*time.Second, func() bool {
		return cl.localJobID("job-y") == "local-1"
	})

	// Blip: the connection dies under the running dispatch.
	hub.dropConn()
	select {
	case <-dispatchDone:
		t.Fatal("dispatch returned when its connection died, before the job was terminal")
	case <-time.After(200 * time.Millisecond):
	}

	// The job is still running, so the dispatch is still alive; the reconnect now
	// goes through and must carry the job in its inflight list.
	gate <- struct{}{}
	hub.waitRegisters(t, 2, 5*time.Second)
	inflight := hub.registerFrames()[1].Inflight
	if len(inflight) != 1 || inflight[0].JobID != "job-y" || inflight[0].Status != job.StatusRunning {
		t.Fatalf("reconnecting register inflight = %+v, want the still-running job-y", inflight)
	}

	// Now the job finishes. The dispatch (which never returned) sends its Result on
	// the live connection.
	jobs.finish(job.JobResult{ID: "local-1", Status: job.StatusDone, ExitCode: 0})
	select {
	case <-dispatchDone:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch did not finish after the local job completed")
	}
	res := hub.waitResult(t, "job-y", 3*time.Second)
	if res.Status != job.StatusDone || res.ExitCode != 0 {
		t.Fatalf("result = %+v, want done/0", res)
	}
	if _, ok := cl.inflightResult("job-y"); ok {
		t.Fatal("the delivered Result is still cached: the in-flight entry must be dropped once it lands")
	}
	got := logs()
	for _, want := range []string{"worker.job_recovering", "worker.job_resumed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("no %s event logged; got:\n%s", want, got)
		}
	}
}

// TestInflightResultCacheExpires pins the bound on the cached-Result table
// (RECOV-01 #5): an undeliverable Result is kept for a bounded time, not forever —
// once the hub's own recovery window has passed it has already failed the job, so
// the entry is dropped instead of being replayed into a finished job (and the table
// of a worker whose hub never comes back stays bounded).
func TestInflightResultCacheExpires(t *testing.T) {
	cl := New(Config{WorkerID: "w1", Token: "t"}, newRunningJobs(t))
	cl.inflightCreate("job-t")
	cl.inflightCacheResult("job-t", wsproto.Result{JobID: "job-t", Status: job.StatusDone})

	if _, ok := cl.inflightResult("job-t"); !ok {
		t.Fatal("a freshly cached Result must be replayable")
	}

	// Age it past the TTL (2x the server's default recovery window).
	cl.inflMu.Lock()
	cl.inflight["job-t"].resultAt = time.Now().Add(-3 * workerResultTTL)
	cl.inflMu.Unlock()

	// Replaying is pointless now, and the register snapshot must not resurrect it.
	if _, ok := cl.inflightResult("job-t"); ok {
		t.Fatal("an expired Result must not be replayed")
	}
	cl.inflightCreate("job-t2")
	cl.inflightCacheResult("job-t2", wsproto.Result{JobID: "job-t2", Status: job.StatusDone})
	cl.inflMu.Lock()
	cl.inflight["job-t2"].resultAt = time.Now().Add(-3 * workerResultTTL)
	cl.inflMu.Unlock()
	if got := cl.inflightSnapshot(); len(got) != 0 {
		t.Fatalf("snapshot = %+v, want the expired entries swept", got)
	}
	if got := cl.inflightIDs(); len(got) != 0 {
		t.Fatalf("in-flight ids = %v, want none after the sweep", got)
	}
}

// drainFrames empties a frame channel (non-blocking).
func drainFrames(ch chan wsproto.Envelope) []wsproto.Envelope {
	var out []wsproto.Envelope
	for {
		select {
		case env := <-ch:
			out = append(out, env)
		default:
			return out
		}
	}
}
