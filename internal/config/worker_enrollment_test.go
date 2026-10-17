package config

import (
	"strings"
	"testing"

	yaml "github.com/goccy/go-yaml"
)

func TestWorkerEnrollmentDefaultsAndDecode(t *testing.T) {
	var zero WorkerEnrollmentConfig
	if zero.Enabled || zero.EffectiveDecideSec() != 120 || zero.EffectiveCollectSec() != 60 ||
		zero.EffectivePerSourcePerMin() != 3 || zero.EffectiveMaxPending() != 20 {
		t.Fatalf("defaults wrong: %+v", zero)
	}
	// The documented shape of the example's commented block decodes onto the struct.
	cfg := &Config{}
	err := yaml.Unmarshal([]byte(`
server:
  worker_enrollment:
    enabled: true
    decide_sec: 90
    collect_sec: 45
    per_source_per_min: 5
    max_pending: 30
    trusted_proxies: ["127.0.0.1/32", "10.0.0.0/8"]
`), cfg)
	if err != nil {
		t.Fatal(err)
	}
	w := cfg.Server.WorkerEnrollment
	if !w.Enabled || w.DecideSec != 90 || w.CollectSec != 45 || w.PerSourcePerMin != 5 || w.MaxPending != 30 || len(w.TrustedProxies) != 2 {
		t.Fatalf("decoded %+v", w)
	}
	if got := len(w.TrustedProxyPrefixes()); got != 2 {
		t.Fatalf("prefixes=%d", got)
	}
	if err := w.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerEnrollmentValidate(t *testing.T) {
	bad := map[string]WorkerEnrollmentConfig{
		"decide_sec":         {DecideSec: 29},
		"decide_sec ":        {DecideSec: 601},
		"collect_sec":        {CollectSec: 10},
		"collect_sec ":       {CollectSec: 9999},
		"per_source_per_min": {PerSourcePerMin: 61},
		"max_pending":        {MaxPending: 201},
		"trusted_proxies":    {TrustedProxies: []string{"10.0.0.0/8", "not-a-cidr"}},
		"trusted_proxies ":   {TrustedProxies: []string{"10.0.0.1"}}, // a bare address is not a CIDR
	}
	for name, w := range bad {
		err := w.validate()
		if err == nil || !strings.Contains(err.Error(), strings.TrimSpace(name)) {
			t.Errorf("%s: want a range/CIDR error naming the field, got %v", name, err)
		}
	}
	ok := WorkerEnrollmentConfig{DecideSec: 30, CollectSec: 600, PerSourcePerMin: 60, MaxPending: 200, TrustedProxies: []string{"::1/128"}}
	if err := ok.validate(); err != nil {
		t.Errorf("boundary values rejected: %v", err)
	}
}

func TestWorkerEnrollmentValidatedByConfigValidate(t *testing.T) {
	cfg := &Config{}
	cfg.Server.WorkerEnrollment.TrustedProxies = []string{"bogus"}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "worker_enrollment.trusted_proxies") {
		t.Fatalf("Validate must reject a bad CIDR, got %v", err)
	}
}

func TestWorkerEnrollmentCloneIsIndependent(t *testing.T) {
	orig := &Config{Server: ServerConfig{WorkerEnrollment: WorkerEnrollmentConfig{Enabled: true, TrustedProxies: []string{"10.0.0.0/8"}}}}
	c := orig.Clone()
	c.Server.WorkerEnrollment.TrustedProxies[0] = "192.168.0.0/16"
	c.Server.WorkerEnrollment.Enabled = false
	if orig.Server.WorkerEnrollment.TrustedProxies[0] != "10.0.0.0/8" || !orig.Server.WorkerEnrollment.Enabled {
		t.Fatal("clone shares state with the original")
	}
}

func TestWorkerEnrollmentFieldPolicies(t *testing.T) {
	block, ok := FieldPolicyFor("server.worker_enrollment")
	if !ok || !block.Editable || !block.HotReload {
		t.Fatalf("block policy = %+v ok=%v", block, ok)
	}
	proxies, ok := FieldPolicyFor("server.worker_enrollment.trusted_proxies")
	if !ok || proxies.Editable || !proxies.HotReload {
		t.Fatalf("trusted_proxies must hot-reload but stay out of the console: %+v ok=%v", proxies, ok)
	}
}
