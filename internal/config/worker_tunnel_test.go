package config

import (
	"strings"
	"testing"
	"time"
)

func TestValidateWorkerTunnel(t *testing.T) {
	if err := ValidateWorkerTunnel(WorkerConfig{Tunnel: WorkerTunnelConfig{Allow: []string{"example.com:443", "127.0.0.1:*"}}}); err != nil {
		t.Fatalf("valid tunnel config rejected: %v", err)
	}
	err := ValidateWorkerTunnel(WorkerConfig{Tunnel: WorkerTunnelConfig{Allow: []string{"bad"}}})
	if err == nil || !strings.Contains(err.Error(), "worker.tunnel") {
		t.Fatalf("invalid tunnel config error = %v", err)
	}
}

func TestWorkerTunnelConfigEffective(t *testing.T) {
	for _, tc := range []struct{ n, want int }{{0, 8}, {-1, 8}, {3, 3}} {
		if got := (WorkerTunnelConfig{MaxConns: tc.n}).EffectiveMaxConns(); got != tc.want {
			t.Errorf("EffectiveMaxConns(%d)=%d, want %d", tc.n, got, tc.want)
		}
	}
	if got := (WorkerTunnelConfig{}).DialTimeout(); got != 5*time.Second {
		t.Errorf("default timeout %v", got)
	}
	if got := (WorkerTunnelConfig{DialTimeoutSec: 2}).DialTimeout(); got != 2*time.Second {
		t.Errorf("configured timeout %v", got)
	}
	if !(WorkerTunnelConfig{Allow: []string{"x:1"}}).Enabled() || (WorkerTunnelConfig{}).Enabled() {
		t.Fatal("Enabled mismatch")
	}
}
