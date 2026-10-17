package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func registerTestConfig() *Config {
	return &Config{Projects: map[string]ProjectConfig{
		"bare":  {},
		"multi": {AllowedRunners: []string{"local", "other"}},
	}}
}

func TestWorkerTokenFingerprint(t *testing.T) {
	fp := WorkerTokenFingerprint("gw_abc")
	if len(fp) != 64 || fp != WorkerTokenFingerprint("gw_abc") || fp == WorkerTokenFingerprint("gw_abd") {
		t.Fatalf("unexpected fingerprint %q", fp)
	}
	// sha256("abc")
	if got := WorkerTokenFingerprint("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("fingerprint = %s", got)
	}
}

func TestNewWorkerToken(t *testing.T) {
	a, err := NewWorkerToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewWorkerToken()
	if !strings.HasPrefix(a, "gw_") || len(a) != 3+64 || a == b {
		t.Fatalf("bad tokens %q %q", a, b)
	}
}

func TestRegisterWorkerInto_Basic(t *testing.T) {
	cfg := registerTestConfig()
	labels := []string{"gpu"}
	if err := RegisterWorkerInto(cfg, "w1", labels, []string{"bare", "multi"}, "tok"); err != nil {
		t.Fatal(err)
	}
	labels[0] = "mutated"
	w := cfg.Server.Workers["w1"]
	if w.Token != "tok" || !reflect.DeepEqual(w.Labels, []string{"gpu"}) {
		t.Fatalf("worker = %+v", w)
	}
	if rc := cfg.Runners["w1"]; rc.Type != "worker" || rc.WorkerID != "w1" {
		t.Fatalf("runner = %+v", rc)
	}
	// empty allowlist expands to [local] first
	if got := cfg.Projects["bare"].AllowedRunners; !reflect.DeepEqual(got, []string{"local", "w1"}) {
		t.Fatalf("bare allowed = %v", got)
	}
	if got := cfg.Projects["multi"].AllowedRunners; !reflect.DeepEqual(got, []string{"local", "other", "w1"}) {
		t.Fatalf("multi allowed = %v", got)
	}
}

func TestRegisterWorkerInto_Rejects(t *testing.T) {
	cfg := registerTestConfig()
	for _, id := range []string{"local", "server", "Server", "-bad", "a b", ""} {
		if err := RegisterWorkerInto(cfg, id, nil, nil, "t"); err == nil {
			t.Errorf("id %q accepted", id)
		}
	}
	if len(cfg.Server.Workers) != 0 || len(cfg.Runners) != 0 {
		t.Fatalf("cfg mutated: %+v %+v", cfg.Server.Workers, cfg.Runners)
	}

	if err := RegisterWorkerInto(cfg, "w1", nil, nil, "t"); err != nil {
		t.Fatal(err)
	}
	if err := RegisterWorkerInto(cfg, "w1", nil, nil, "t2"); !errors.Is(err, ErrWorkerAlreadyRegistered) {
		t.Fatalf("duplicate err = %v", err)
	}
	if cfg.Server.Workers["w1"].Token != "t" {
		t.Fatal("duplicate overwrote token")
	}

	cfg.Runners["peer"] = RunnerConfig{Type: "ssh"}
	if err := RegisterWorkerInto(cfg, "peer", nil, nil, "t"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("runner clash err = %v", err)
	}
	if err := RegisterWorkerInto(cfg, "w2", nil, []string{"nope"}, "t"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing project err = %v", err)
	}
	if _, ok := cfg.Server.Workers["w2"]; ok {
		t.Fatal("w2 registered despite error")
	}
}

func TestRemoveWorkerFrom(t *testing.T) {
	cfg := registerTestConfig()
	if err := RegisterWorkerInto(cfg, "w1", nil, []string{"bare", "multi"}, "tok"); err != nil {
		t.Fatal(err)
	}
	removed, err := RemoveWorkerFrom(cfg, "w1", "")
	if !removed || err != nil {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if _, ok := cfg.Server.Workers["w1"]; ok {
		t.Fatal("worker kept")
	}
	if _, ok := cfg.Runners["w1"]; ok {
		t.Fatal("runner kept")
	}
	if got := cfg.Projects["bare"].AllowedRunners; !reflect.DeepEqual(got, []string{"local"}) {
		t.Fatalf("bare allowed = %v", got)
	}
	if got := cfg.Projects["multi"].AllowedRunners; !reflect.DeepEqual(got, []string{"local", "other"}) {
		t.Fatalf("multi allowed = %v", got)
	}
	removed, err = RemoveWorkerFrom(cfg, "w1", "")
	if removed || !errors.Is(err, ErrWorkerNotFound) {
		t.Fatalf("second removal removed=%v err=%v", removed, err)
	}
}

func TestRemoveWorkerFrom_Fingerprint(t *testing.T) {
	cfg := registerTestConfig()
	if err := RegisterWorkerInto(cfg, "w1", nil, []string{"bare"}, "tok"); err != nil {
		t.Fatal(err)
	}
	removed, err := RemoveWorkerFrom(cfg, "w1", WorkerTokenFingerprint("other"))
	if removed || !errors.Is(err, ErrWorkerFingerprintMismatch) {
		t.Fatalf("mismatch removed=%v err=%v", removed, err)
	}
	if _, ok := cfg.Server.Workers["w1"]; !ok {
		t.Fatal("worker removed on mismatch")
	}
	if _, ok := cfg.Runners["w1"]; !ok {
		t.Fatal("runner removed on mismatch")
	}
	if got := cfg.Projects["bare"].AllowedRunners; !reflect.DeepEqual(got, []string{"local", "w1"}) {
		t.Fatalf("allowed changed on mismatch: %v", got)
	}
	removed, err = RemoveWorkerFrom(cfg, "w1", WorkerTokenFingerprint("tok"))
	if !removed || err != nil {
		t.Fatalf("match removed=%v err=%v", removed, err)
	}
}
