package tunnel

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestUDPEndToEndLatency(t *testing.T) {
	echo, stop := udpEcho(t)
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workerURLCh := make(chan string, 1)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ua, _ := net.ResolveUDPAddr("udp", echo)
		pc, _ := net.ListenPacket("udp", "127.0.0.1:0")
		DatagramBridge(ctx, ws, pc, ua)
	}))
	defer worker.Close()
	workerURLCh <- "ws" + worker.URL[len("http"):]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		wu := <-workerURLCh
		workerConn, _, err := websocket.Dial(ctx, wu, nil)
		if err != nil {
			return
		}
		Splice(ctx, client, workerConn, SpliceOptions{})
	}))
	defer server.Close()
	var events = make(chan eventRecord, 32)
	f := &Forwarder{Spec: ForwardSpec{Network: "udp", Bind: "127.0.0.1", LocalPort: 0, Target: echo}, Ready: make(chan error, 1), OnEvent: func(n string, a ...any) { events <- eventRecord{name: n, attrs: a} }, Dial: func(c context.Context) (DialResult, error) {
		c1, _, err := websocket.Dial(c, "ws"+server.URL[len("http"):], nil)
		return DialResult{Conn: c1, TunnelID: "e2e"}, err
	}}
	go f.Run(ctx)
	if err := <-f.Ready; err != nil {
		t.Fatal(err)
	}
	pc, _ := net.Dial("udp", f.ActualAddr)
	defer pc.Close()
	const n = 200
	payload := []byte("1234567")
	rtts := make([]time.Duration, 0, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		ts := time.Now()
		pc.Write(payload)
		got := make([]byte, 32)
		pc.SetReadDeadline(time.Now().Add(5 * time.Second))
		nr, err := pc.Read(got)
		if err != nil || string(got[:nr]) != string(payload) {
			t.Fatalf("echo %d: %v", i, err)
		}
		rtts = append(rtts, time.Since(ts))
	}
	cancel()
	time.Sleep(20 * time.Millisecond)
	var dial, up, down int64
	var bu int64
	for {
		select {
		case e := <-events:
			switch e.name {
			case "tunnel.opened":
				dial = attrInt(e.attrs, "dial_ms")
			case "tunnel.first_up":
				up = attrInt(e.attrs, "first_byte_ms")
			case "tunnel.first_down":
				down = attrInt(e.attrs, "first_byte_ms")
			case "session.closed":
				bu = attrInt(e.attrs, "bytes_up")
			}
		default:
			goto done
		}
	}
done:
	t.Logf("udp_e2e pool=%s n=%d dial_ms=%d first_up_ms=%d first_down_ms=%d rtt_p50_ms=%.3f rtt_p95_ms=%.3f rtt_max_ms=%.3f total_ms=%d bytes_up=%d bytes_down=%d", poolLabel(), n, dial, up, down, e2ePercentileMillis(rtts, .5), e2ePercentileMillis(rtts, .95), e2ePercentileMillis(rtts, 1), time.Since(start).Milliseconds(), bu, bu)
	if len(rtts) != n || bu != n*int64(len(payload)) {
		t.Fatalf("counts n=%d bytes_up=%d", len(rtts), bu)
	}
}

type eventRecord struct {
	name  string
	attrs []any
}

func attrInt(a []any, key string) int64 {
	for i := 0; i+1 < len(a); i += 2 {
		if a[i] == key {
			if v, ok := a[i+1].(int64); ok {
				return v
			}
			if v, ok := a[i+1].(int); ok {
				return int64(v)
			}
		}
	}
	return 0
}
func poolLabel() string {
	if !udpBufferPoolEnabled {
		return "off"
	}
	return "on"
}
func e2ePercentileMillis(v []time.Duration, p float64) float64 {
	s := append([]time.Duration(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return float64(s[int(float64(len(s)-1)*p)].Microseconds()) / 1000
}
