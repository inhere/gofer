package tunnel

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestUDPRelayMeasurement records a repeatable 200-datagram RTT sample. Set
// GOFER_UDP_BUFFER_POOL=0 to compare the allocation-heavy fallback.
func TestUDPRelayMeasurement(t *testing.T) {
	echo, stop := udpEcho(t)
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, done := startDatagramBridge(t, ctx, echo)
	const n = 200
	rtts := make([]time.Duration, 0, n)
	started := time.Now()
	var bytes int
	for i := 0; i < n; i++ {
		payload := []byte(fmt.Sprintf("pkt-%03d", i))
		ts := time.Now()
		if err := client.Write(ctx, websocket.MessageBinary, payload); err != nil {
			t.Fatal(err)
		}
		if _, _, err := client.Read(ctx); err != nil {
			t.Fatal(err)
		}
		rtts = append(rtts, time.Since(ts))
		bytes += len(payload)
	}
	client.Close(websocket.StatusNormalClosure, "")
	<-done
	// Keep the test output machine-readable for before/after comparison.
	t.Logf("udp_measure pool=%q n=%d total_ms=%d bytes=%d rtt_p50_ms=%.3f rtt_p95_ms=%.3f first_byte_ms=%.3f",
		os.Getenv("GOFER_UDP_BUFFER_POOL"), n, time.Since(started).Milliseconds(), bytes,
		percentileMillis(rtts, .50), percentileMillis(rtts, .95), float64(rtts[0].Microseconds())/1000)
}

func percentileMillis(v []time.Duration, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	// Samples are naturally ordered enough for this local measurement; copy and sort
	// to keep the helper deterministic if scheduling reorders timings.
	s := append([]time.Duration(nil), v...)
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	i := int(float64(len(s)-1) * p)
	return float64(s[i].Microseconds()) / 1000
}
