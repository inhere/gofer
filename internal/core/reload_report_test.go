package core

import (
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

func TestReloadReportsRestartRequiredKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	root := t.TempDir()
	writeConfig(t, path, &config.Config{Storage: config.StorageConfig{Root: root}, Server: config.ServerConfig{Addr: "127.0.0.1:1", MaxJobTimeoutSec: 10}})
	cfg, _, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cr, err := Build(cfg, WithConfigPath(path), WithAgentDetector(agent.NoopDetector{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cr.Close() }()
	writeConfig(t, path, &config.Config{Storage: config.StorageConfig{Root: root}, Server: config.ServerConfig{Addr: "127.0.0.1:2", MaxJobTimeoutSec: 20}})
	report, err := cr.ReloadDetailed(path)
	if err != nil {
		t.Fatal(err)
	}
	if report.Rev <= 1 {
		t.Fatalf("rev=%d, want incremented revision", report.Rev)
	}
	if len(report.Changed) == 0 || report.Changed[0] != "server" {
		t.Fatalf("changed=%v, want server partition", report.Changed)
	}
	if len(report.RestartRequired) != 1 || report.RestartRequired[0] != "server.addr" {
		t.Fatalf("restart_required=%v, want [server.addr]", report.RestartRequired)
	}
}

func TestReloadReportsPeerRunnerRestartRequired(t *testing.T) {
	old := &config.Config{Runners: map[string]config.RunnerConfig{
		"peer":   {Type: "peer-http", BaseURL: "http://old"},
		"worker": {Type: "worker", WorkerID: "w1"},
	}}
	next := old.Clone()
	next.Runners = map[string]config.RunnerConfig{}
	for name, rc := range old.Runners {
		next.Runners[name] = rc
	}
	next.Runners["peer"] = config.RunnerConfig{Type: "peer-http", BaseURL: "http://new"}
	next.Runners["worker-2"] = config.RunnerConfig{Type: "worker", WorkerID: "w2"}
	result := reloadResult(old, next, "config.yaml")
	if !containsString(result.RestartRequired, "runners.peer") {
		t.Fatalf("restart_required=%v, want runners.peer", result.RestartRequired)
	}
	if containsString(result.RestartRequired, "runners.worker-2") {
		t.Fatalf("worker runner addition incorrectly requires restart: %v", result.RestartRequired)
	}
}

func TestReloadReportsWorkersHotAndCallersRestart(t *testing.T) {
	old := &config.Config{Server: config.ServerConfig{
		Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "one"}},
		Callers: []config.CallerConfig{{ID: "operator", Token: "one"}},
	}}
	next := old.Clone()
	next.Server.Workers = map[string]config.WorkerAuthConfig{
		"w1": {Token: "one"}, "w2": {Token: "two"},
	}
	next.Server.Callers = []config.CallerConfig{
		{ID: "operator", Token: "one"}, {ID: "ci", Token: "ci-token"},
	}
	result := reloadResult(old, next, "config.yaml")
	if containsString(result.RestartRequired, "server.workers") {
		t.Fatalf("server.workers is hot-reloaded and must not require restart: %v", result.RestartRequired)
	}
	if !containsString(result.RestartRequired, "server.callers") {
		t.Fatalf("server.callers still rebuilds auth at startup and must require restart: %v", result.RestartRequired)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
