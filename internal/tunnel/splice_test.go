package tunnel

import (
	"context"
	"github.com/coder/websocket"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// spliceTestWait bounds how long a test waits for Splice to return. It is an upper
// bound, not an expected duration (passing runs take milliseconds): closing both
// websockets can take noticeably longer when the whole suite runs in parallel, and
// the websocket close handshake alone may wait up to 5s.
const spliceTestWait = 10 * time.Second

func spliceEndpoint(t *testing.T, ch chan<- *websocket.Conn) (*websocket.Conn, func()) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e == nil {
			ch <- c
		}
	}))
	c, _, e := websocket.Dial(context.Background(), "ws"+s.URL[4:], nil)
	if e != nil {
		t.Fatal(e)
	}
	return c, s.Close
}

func TestSpliceBidirectionalProgress(t *testing.T) {
	ca, cb := make(chan *websocket.Conn, 1), make(chan *websocket.Conn, 1)
	a, closeA := spliceEndpoint(t, ca)
	defer closeA()
	b, closeB := spliceEndpoint(t, cb)
	defer closeB()
	sa, sb := <-ca, <-cb
	var mu sync.Mutex
	var prog [][2]int64
	done := make(chan SpliceResult, 1)
	go func() {
		done <- Splice(context.Background(), sa, sb, SpliceOptions{OnProgress: func(u, d int64) { mu.Lock(); prog = append(prog, [2]int64{u, d}); mu.Unlock() }})
	}()
	x := []byte("hello")
	y := []byte("world!")
	rd := make(chan struct{}, 2)
	// drain signals once the forwarded message arrived, then keeps reading so the
	// close handshake Splice starts on teardown completes without the 5s timeout.
	drain := func(c *websocket.Conn) {
		signalled := false
		for {
			_, r, err := c.Reader(context.Background())
			if err == nil {
				_, _ = io.ReadAll(r)
			}
			if !signalled {
				signalled = true
				rd <- struct{}{}
			}
			if err != nil {
				return
			}
		}
	}
	go drain(a)
	go drain(b)
	a.Write(context.Background(), websocket.MessageBinary, x)
	b.Write(context.Background(), websocket.MessageBinary, y)
	// Both messages must be delivered before the client closes; otherwise the
	// worker->client write races the close and the splice ends as worker_closed.
	<-rd
	<-rd
	a.Close(websocket.StatusNormalClosure, "")
	r := <-done
	if r.Up != int64(len(x)) || r.Down != int64(len(y)) {
		t.Fatalf("%+v", r)
	}
	if r.Reason != "client_closed" {
		t.Fatal(r.Reason)
	}
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(prog); i++ {
		if prog[i][0] < prog[i-1][0] || prog[i][1] < prog[i-1][1] {
			t.Fatal("non monotonic")
		}
	}
}

func TestSpliceWorkerClosedAndCtx(t *testing.T) {
	ca, cb := make(chan *websocket.Conn, 1), make(chan *websocket.Conn, 1)
	a, caClose := spliceEndpoint(t, ca)
	defer caClose()
	b, cbClose := spliceEndpoint(t, cb)
	defer cbClose()
	sa, sb := <-ca, <-cb
	d := make(chan SpliceResult, 1)
	go func() { d <- Splice(context.Background(), sa, sb, SpliceOptions{}) }()
	b.Close(websocket.StatusNormalClosure, "")
	select {
	case r := <-d:
		if r.Reason != "worker_closed" {
			t.Fatal(r.Reason)
		}
	case <-time.After(spliceTestWait):
		t.Fatal("timeout")
	}
	ca, cb = make(chan *websocket.Conn, 1), make(chan *websocket.Conn, 1)
	a, caClose = spliceEndpoint(t, ca)
	defer caClose()
	b, cbClose = spliceEndpoint(t, cb)
	defer cbClose()
	sa, sb = <-ca, <-cb
	ctx, cancel := context.WithCancel(context.Background())
	d = make(chan SpliceResult, 1)
	go func() { d <- Splice(ctx, sa, sb, SpliceOptions{}) }()
	tm := time.NewTimer(10 * time.Millisecond)
	<-tm.C
	cancel()
	select {
	case r := <-d:
		if r.Reason != "ctx_done" {
			t.Fatal(r.Reason)
		}
	case <-time.After(spliceTestWait):
		t.Fatal("timeout")
	}
	_ = a
	_ = b
}

func TestSplicePingTimeout(t *testing.T) {
	ca, cb := make(chan *websocket.Conn, 1), make(chan *websocket.Conn, 1)
	a, caClose := spliceEndpoint(t, ca)
	defer caClose()
	b, cbClose := spliceEndpoint(t, cb)
	defer cbClose()
	sa, sb := <-ca, <-cb
	d := make(chan SpliceResult, 1)
	go func() {
		d <- Splice(context.Background(), sa, sb, SpliceOptions{PingInterval: 20 * time.Millisecond, PingTimeout: 20 * time.Millisecond})
	}()
	select {
	case r := <-d:
		if r.Reason != "ping_timeout" {
			t.Fatal(r.Reason)
		}
	case <-time.After(spliceTestWait):
		t.Fatal("timeout")
	}
	_ = a
	_ = b
}
