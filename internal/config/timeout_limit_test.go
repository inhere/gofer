package config

import "testing"

func TestEffectiveMaxTimeoutSource(t *testing.T) {
	cfg := &Config{
		Server:   ServerConfig{MaxJobTimeoutSec: 900},
		Projects: map[string]ProjectConfig{"p": {MaxTimeoutSec: 7200}, "q": {}},
	}
	for key, want := range map[string]struct {
		sec int
		src string
	}{"p": {7200, TimeoutLimitProject}, "q": {900, TimeoutLimitServer}, "unknown": {900, TimeoutLimitServer}} {
		if sec, src := cfg.EffectiveMaxTimeout(key); sec != want.sec || src != want.src {
			t.Errorf("%s: got (%d,%s), want (%d,%s)", key, sec, src, want.sec, want.src)
		}
	}
	if sec, src := (&Config{}).EffectiveMaxTimeout("x"); sec != DefaultMaxJobTimeoutSec || src != TimeoutLimitServer {
		t.Errorf("default: (%d,%s)", sec, src)
	}
	if TimeoutLimitKey(TimeoutLimitProject, "p") != "projects.p.max_timeout_sec" || TimeoutLimitKey(TimeoutLimitServer, "p") != "server.max_job_timeout_sec" || TimeoutLimitKey("", "p") != "" {
		t.Error("TimeoutLimitKey")
	}
}
