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
