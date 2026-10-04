package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/config"
)

type registrationWriter struct {
	cfg *config.Config
}

func (w *registrationWriter) Update(mut func(*config.Config) error) error {
	if err := mut(w.cfg); err != nil {
		return err
	}
	return nil
}

func (w *registrationWriter) ReloadConfig() error { return nil }

func TestRegisterWorkerConflict(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{
		Token:      "user-token",
		Governance: config.GovernanceConfig{RequireAdminCapability: true},
		Callers:    []config.CallerConfig{{ID: "admin", Token: "admin-token", CanAdmin: true}},
		Workers:    map[string]config.WorkerAuthConfig{"w1": {Token: "old-token"}},
	}, Runners: map[string]config.RunnerConfig{"w1": {Type: "worker", WorkerID: "w1"}}}
	s := newRegistrationServer(t, cfg)
	body := map[string]any{"worker_id": "w1"}
	resp := do(t, s, http.MethodPost, "/v1/workers", "admin-token", body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d, want 409: %s", resp.StatusCode, bodyText(t, resp))
	}
}

func TestRegisterWorkerSavesOnlyDeclaredKeys(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{
		Token:      "user-token",
		Governance: config.GovernanceConfig{RequireAdminCapability: true},
		Callers:    []config.CallerConfig{{ID: "admin", Token: "admin-token", CanAdmin: true}},
	}, Runners: map[string]config.RunnerConfig{}, Projects: map[string]config.ProjectConfig{
		"p": {AllowedRunners: []string{"local"}},
	}}
	s := newRegistrationServer(t, cfg)
	resp := do(t, s, http.MethodPost, "/v1/workers", "admin-token", map[string]any{
		"worker_id": "w-new", "labels": []string{"linux"}, "projects": []string{"p"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d, want 201: %s", resp.StatusCode, bodyText(t, resp))
	}
	var got workerRegistrationResp
	decode(t, resp, &got)
	if got.WorkerToken == "" || got.WorkerConnectURL == "" {
		t.Fatalf("registration response=%+v, want one-time token and connect URL", got)
	}
	if cfg.Server.Workers["w-new"].Token != got.WorkerToken {
		t.Fatalf("worker token not saved in config: %+v", cfg.Server.Workers)
	}
	runner, ok := cfg.Runners["w-new"]
	if !ok || runner.Type != "worker" || runner.WorkerID != "w-new" {
		t.Fatalf("worker runner missing: %+v", cfg.Runners)
	}
	if len(cfg.Projects["p"].AllowedRunners) != 2 || cfg.Projects["p"].AllowedRunners[1] != "w-new" {
		t.Fatalf("project allowlist=%v", cfg.Projects["p"].AllowedRunners)
	}
}

func TestRegisterWorkerKeepsImplicitLocalRunner(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{
		Token:      "user-token",
		Governance: config.GovernanceConfig{RequireAdminCapability: true},
		Callers:    []config.CallerConfig{{ID: "admin", Token: "admin-token", CanAdmin: true}},
	}, Runners: map[string]config.RunnerConfig{}, Projects: map[string]config.ProjectConfig{"p": {}}}
	s := newRegistrationServer(t, cfg)
	resp := do(t, s, http.MethodPost, "/v1/workers", "admin-token", map[string]any{
		"worker_id": "w-new", "projects": []string{"p"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d, want 201: %s", resp.StatusCode, bodyText(t, resp))
	}
	got := cfg.Projects["p"].AllowedRunners
	if len(got) != 2 || got[0] != config.BuiltinLocalRunner || got[1] != "w-new" {
		t.Fatalf("project allowlist=%v, want [local w-new]", got)
	}
}

func TestRemoveWorkerDisconnects(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{
		Token:      "user-token",
		Governance: config.GovernanceConfig{RequireAdminCapability: true},
		Callers:    []config.CallerConfig{{ID: "admin", Token: "admin-token", CanAdmin: true}},
		Workers:    map[string]config.WorkerAuthConfig{"w1": {Token: "worker-token"}},
	}, Runners: map[string]config.RunnerConfig{"w1": {Type: "worker", WorkerID: "w1"}}, Projects: map[string]config.ProjectConfig{
		"p": {AllowedRunners: []string{"local", "w1"}},
	}}
	s := newRegistrationServer(t, cfg)
	resp := do(t, s, http.MethodDelete, "/v1/workers/w1", "admin-token", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d, want 204: %s", resp.StatusCode, bodyText(t, resp))
	}
	if _, ok := cfg.Server.Workers["w1"]; ok {
		t.Fatal("worker auth still present")
	}
	if _, ok := cfg.Runners["w1"]; ok {
		t.Fatal("worker runner still present")
	}
	if got := cfg.Projects["p"].AllowedRunners; len(got) != 1 || got[0] != "local" {
		t.Fatalf("project allowlist=%v", got)
	}
}

func newRegistrationServer(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	s := newAssignableServer(t)
	s.cfg = &cfg.Server
	s.token = "admin-token"
	s.callers = []callerEntry{{id: "admin", token: "admin-token", kind: callerKindUser}}
	s.SetConfigWriter(&registrationWriter{cfg: cfg})
	s.SetBuildInfo(buildinfo.Info{Version: "v0.99"})
	return s
}

// "server" / "local" name the built-in runner, so they can never be a worker id.
func TestRegisterWorkerRejectsReservedID(t *testing.T) {
	for _, id := range []string{"server", "local", "Server", " LOCAL "} {
		cfg := &config.Config{Server: config.ServerConfig{
			Token:      "user-token",
			Governance: config.GovernanceConfig{RequireAdminCapability: true},
			Callers:    []config.CallerConfig{{ID: "admin", Token: "admin-token", CanAdmin: true}},
		}, Runners: map[string]config.RunnerConfig{}}
		s := newRegistrationServer(t, cfg)
		resp := do(t, s, http.MethodPost, "/v1/workers", "admin-token", map[string]any{"worker_id": id})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("id %q: status=%d, want 400: %s", id, resp.StatusCode, bodyText(t, resp))
		}
		if len(cfg.Server.Workers) != 0 || len(cfg.Runners) != 0 {
			t.Fatalf("id %q: config was mutated: %+v %+v", id, cfg.Server.Workers, cfg.Runners)
		}
	}
}
