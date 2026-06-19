package wshub

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/wsproto"
)

// fakeSink records WriteLog/Finish calls in order for ordering + back-pressure
// assertions. It is concurrency-safe (the hub read loop calls it from one
// goroutine, the test reads from another).
type fakeSink struct {
	mu       sync.Mutex
	events   []string // "log:<text>" / "finish:<status>" / "disconnect:<err>" in arrival order
	finished chan wsproto.Result
	lost     chan error
}

func newFakeSink() *fakeSink {
	return &fakeSink{finished: make(chan wsproto.Result, 1), lost: make(chan error, 1)}
}

func (s *fakeSink) WriteLog(_ string, _ int, text string) {
	s.mu.Lock()
	s.events = append(s.events, "log:"+text)
	s.mu.Unlock()
}

func (s *fakeSink) OnInteraction(action string, _ json.RawMessage) {
	s.mu.Lock()
	s.events = append(s.events, "interaction:"+action)
	s.mu.Unlock()
}

func (s *fakeSink) Finish(res wsproto.Result) {
	s.mu.Lock()
	s.events = append(s.events, "finish:"+res.Status)
	s.mu.Unlock()
	select {
	case s.finished <- res:
	default:
	}
}

func (s *fakeSink) OnDisconnect(err error) {
	s.mu.Lock()
	s.events = append(s.events, "disconnect:"+err.Error())
	s.mu.Unlock()
	select {
	case s.lost <- err:
	default:
	}
}

func (s *fakeSink) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

// hubServer stands up a rux router mounting hub.Accept under a fixed callerID
// and returns the httptest server + ws URL.
func hubServer(t *testing.T, hub *Hub, callerID string) (*httptest.Server, string) {
	t.Helper()
	r := rux.New()
	r.GET("/v1/workers/connect", func(c *rux.Context) {
		hub.Accept(c.Resp, c.Req, callerID)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workers/connect"
	return srv, wsURL
}

// dialAndRegister dials the hub, sends a register frame and reads the registered
// ack. It returns the live conn + the ack.
func dialAndRegister(t *testing.T, ctx context.Context, wsURL, workerID string) (*websocket.Conn, wsproto.Registered) {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.SetReadLimit(1 << 20)
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type:    wsproto.TypeRegister,
		Payload: mustRaw(wsproto.Register{WorkerID: workerID}),
	}); err != nil {
		t.Fatalf("write register: %v", err)
	}
	env, err := readEnvelope(ctx, conn)
	if err != nil {
		t.Fatalf("read registered: %v", err)
	}
	reg, _ := wsproto.As[wsproto.Registered](env)
	return conn, reg
}

func TestRegistryPutGetRemove(t *testing.T) {
	r := newRegistry()
	a := &workerConn{workerID: "w1", sinks: map[string]JobSink{}}
	b := &workerConn{workerID: "w1", sinks: map[string]JobSink{}}

	if old := r.Put(a); old != nil {
		t.Fatal("first Put should return nil old")
	}
	if old := r.Put(b); old != a {
		t.Fatal("re-Put same worker_id should return the prior conn")
	}
	if got, ok := r.Get("w1"); !ok || got != b {
		t.Fatal("Get should return the latest conn")
	}
	// Remove with the OLD conn must not evict the current (b).
	r.Remove("w1", a)
	if _, ok := r.Get("w1"); !ok {
		t.Fatal("Remove with stale conn must not evict the replacement")
	}
	r.Remove("w1", b)
	if _, ok := r.Get("w1"); ok {
		t.Fatal("Remove with current conn should evict")
	}
}

// TestRegistryConcurrent exercises concurrent Put/Get/Remove for -race.
func TestRegistryConcurrent(t *testing.T) {
	r := newRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			wc := &workerConn{workerID: "w", sinks: map[string]JobSink{}}
			r.Put(wc)
			r.Get("w")
			r.Remove("w", wc)
		}(i)
	}
	wg.Wait()
}

func TestRegisterAccepted(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, reg := dialAndRegister(t, ctx, wsURL, "w1")
	defer conn.Close(websocket.StatusNormalClosure, "")
	if !reg.Accepted {
		t.Fatalf("expected accepted, got %+v", reg)
	}
	if reg.ServerTime < 1_000_000_000_000 {
		t.Fatalf("server_time not millis: %d", reg.ServerTime)
	}
	// The worker is now in the registry.
	waitFor(t, func() bool { _, ok := hub.reg.Get("w1"); return ok })
}

func TestRegisterTokenBindingMismatch(t *testing.T) {
	// Bind w1→w1 but the connection authenticates as caller "other": register w1
	// must be rejected (review #1).
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "other")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, reg := dialAndRegister(t, ctx, wsURL, "w1")
	defer conn.Close(websocket.StatusNormalClosure, "")
	if reg.Accepted {
		t.Fatal("expected rejection for token-binding mismatch")
	}
	if reg.Reason == "" {
		t.Fatal("expected a rejection reason")
	}
	// Connection should be closed and the worker not registered.
	if _, ok := hub.reg.Get("w1"); ok {
		t.Fatal("rejected worker must not be in registry")
	}
}

func TestDispatchOfflineWorker(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: "j1"}); err != ErrWorkerOffline {
		t.Fatalf("expected ErrWorkerOffline, got %v", err)
	}
	if err := hub.RegisterSink("w1", "j1", newFakeSink()); err != ErrWorkerOffline {
		t.Fatalf("RegisterSink offline expected ErrWorkerOffline, got %v", err)
	}
}

// TestReadLoopOrdering (review #2 core): a worker pushes log(1) log(2) result on
// one connection; the sink must observe them in exactly that order — Finish only
// after both WriteLog. Runs over a real loopback with -race.
func TestReadLoopOrdering(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, reg := dialAndRegister(t, ctx, wsURL, "w1")
	defer conn.Close(websocket.StatusNormalClosure, "")
	if !reg.Accepted {
		t.Fatal("register rejected")
	}

	sink := newFakeSink()
	// Register the sink BEFORE the worker pushes anything (sink-before-dispatch).
	if err := hub.RegisterSink("w1", "j1", sink); err != nil {
		t.Fatalf("RegisterSink: %v", err)
	}

	push := func(t wsproto.FrameType, payload any) {
		if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: t, JobID: "j1", Payload: mustRaw(payload)}); err != nil {
			// loopback fatal
			panic(err)
		}
	}
	push(wsproto.TypeLog, wsproto.Log{JobID: "j1", Stream: "stdout", Seq: 1, Text: "one"})
	push(wsproto.TypeLog, wsproto.Log{JobID: "j1", Stream: "stdout", Seq: 2, Text: "two"})
	push(wsproto.TypeResult, wsproto.Result{JobID: "j1", Status: "done", ExitCode: 0})

	select {
	case <-sink.finished:
	case <-ctx.Done():
		t.Fatal("did not observe finish")
	}

	got := sink.snapshot()
	want := []string{"log:one", "log:two", "finish:done"}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] = %q, want %q (order=%v)", i, got[i], want[i], got)
		}
	}
}

// TestSinkNotRegisteredDropsFrame proves a frame for an unregistered job is
// dropped (not a panic): the hub stays alive and a later result for a registered
// sink still arrives.
func TestSinkNotRegisteredDropsFrame(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, reg := dialAndRegister(t, ctx, wsURL, "w1")
	defer conn.Close(websocket.StatusNormalClosure, "")
	if !reg.Accepted {
		t.Fatal("register rejected")
	}

	// Push a frame for an unregistered job — must be silently dropped.
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type: wsproto.TypeLog, JobID: "ghost",
		Payload: mustRaw(wsproto.Log{JobID: "ghost", Stream: "stdout", Text: "x"}),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Now register a real sink and finish it; the hub must still be reading.
	sink := newFakeSink()
	if err := hub.RegisterSink("w1", "j1", sink); err != nil {
		t.Fatalf("RegisterSink: %v", err)
	}
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{
		Type: wsproto.TypeResult, JobID: "j1",
		Payload: mustRaw(wsproto.Result{JobID: "j1", Status: "done"}),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case <-sink.finished:
	case <-ctx.Done():
		t.Fatal("hub stopped reading after dropping an unregistered frame")
	}
}

// TestBackPressureChattyJob (review #3): a chatty job streams many frames while a
// second job on the SAME connection must still get its result promptly (HOL not
// blocked), and memory stays bounded (the sink does not buffer all history).
func TestBackPressureChattyJob(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, reg := dialAndRegister(t, ctx, wsURL, "w1")
	defer conn.Close(websocket.StatusNormalClosure, "")
	if !reg.Accepted {
		t.Fatal("register rejected")
	}

	chatty := newFakeSink()
	quiet := newFakeSink()
	if err := hub.RegisterSink("w1", "chatty", chatty); err != nil {
		t.Fatal(err)
	}
	if err := hub.RegisterSink("w1", "quiet", quiet); err != nil {
		t.Fatal(err)
	}

	// Interleave a flood of chatty log frames with a single quiet result.
	go func() {
		big := strings.Repeat("x", 4096)
		for i := 0; i < 500; i++ {
			_ = wsjson.Write(ctx, conn, wsproto.Envelope{
				Type: wsproto.TypeLog, JobID: "chatty",
				Payload: mustRaw(wsproto.Log{JobID: "chatty", Stream: "stdout", Seq: i, Text: big}),
			})
			if i == 5 {
				// Send the quiet job's result early in the flood.
				_ = wsjson.Write(ctx, conn, wsproto.Envelope{
					Type: wsproto.TypeResult, JobID: "quiet",
					Payload: mustRaw(wsproto.Result{JobID: "quiet", Status: "done"}),
				})
			}
		}
	}()

	// The quiet job's result must not be starved by the chatty flood.
	select {
	case <-quiet.finished:
	case <-ctx.Done():
		t.Fatal("quiet job's result starved by chatty job (HOL block)")
	}
}

// waitFor polls cond up to ~2s; fails the test if it never becomes true.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
