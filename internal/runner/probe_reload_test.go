package runner

import (
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

func TestRunnerProbeHotReload(t *testing.T) {
	p := NewPeerProber(&config.Config{Runners: map[string]config.RunnerConfig{
		"peer": {Type: "peer-http", BaseURL: "http://old"},
	}}, time.Second)
	if p == nil || p.TargetCount() != 1 {
		t.Fatalf("initial prober=%v, want one target", p)
	}
	p.Reload(&config.Config{Runners: map[string]config.RunnerConfig{
		"peer":  {Type: "peer-http", BaseURL: "http://new"},
		"peer2": {Type: "peer-http", BaseURL: "http://new2"},
	}}, 2*time.Second)
	if got := p.TargetCount(); got != 2 {
		t.Fatalf("target count after reload=%d, want 2", got)
	}
}
