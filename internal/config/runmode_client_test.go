package config

import (
	"path/filepath"
	"testing"
)

// clientModeConfigYAML is a minimal VALID config (load-level validate only needs a
// non-empty host_path) carrying a project key, so a client-mode test can prove the
// file was NOT read by the key it would have contributed.
func clientModeConfigYAML(key string) string {
	return "server:\n  addr: 127.0.0.1:9999\nprojects:\n  " + key + ":\n    host_path: /tmp/" + key + "\n"
}

// TestRunModeClientParsed proves GOFER_RUN_MODE=client is recognized
// case-insensitively (and trimmed) while every other value still resolves to the
// server default (E38② role set + C1).
func TestRunModeClientParsed(t *testing.T) {
	cases := []struct {
		env  string
		want string
	}{
		{"client", RunModeClient},
		{"Client", RunModeClient},
		{"  CLIENT ", RunModeClient},
		{"server", RunModeServer},
		{"worker", RunModeWorker},
		{"", RunModeServer},
		{"garbage", RunModeServer},
	}
	for _, tc := range cases {
		t.Setenv(EnvRunMode, tc.env)
		if got := RunMode(); got != tc.want {
			t.Fatalf("RunMode() with %q = %q, want %q", tc.env, got, tc.want)
		}
	}

	t.Setenv(EnvRunMode, "CLIENT")
	if !IsClientRunMode() {
		t.Fatal("IsClientRunMode() must be true for GOFER_RUN_MODE=CLIENT")
	}
	t.Setenv(EnvRunMode, RunModeServer)
	if IsClientRunMode() {
		t.Fatal("IsClientRunMode() must be false for the server role")
	}
}

// TestLoadSkipsLocalFilesInClientMode proves client mode never reads the
// auto-discovered local configs: both a cwd ./.gofer.yaml and the user-level
// <config-dir>/config.yaml exist and would be picked up, yet Load returns an empty
// defaulted Config with an empty path and no error (the normal client-node state).
func TestLoadSkipsLocalFilesInClientMode(t *testing.T) {
	work := t.TempDir()
	cfgDir := t.TempDir()
	t.Setenv(EnvConfigDir, cfgDir)
	t.Setenv(EnvRunMode, RunModeClient)
	t.Setenv(EnvConfigPath, "") // no explicit env pointer: discovery tier only

	write(t, filepath.Join(work, ".gofer.yaml"), clientModeConfigYAML("from-cwd"))
	write(t, filepath.Join(cfgDir, "config.yaml"), clientModeConfigYAML("from-config-dir"))

	chdir(t, work) // registered after TempDir: restored before its removal (Windows)

	cfg, path, err := Load("")
	if err != nil {
		t.Fatalf("Load in client mode returned an error: %v", err)
	}
	if path != "" {
		t.Fatalf("client mode must not resolve a config path, got %q", path)
	}
	if len(cfg.Projects) != 0 {
		t.Fatalf("client mode must not read local projects, got %v", cfg.Projects)
	}
	if cfg.Server.Addr == "127.0.0.1:9999" {
		t.Fatal("client mode read server.addr from a local config file")
	}
}

// TestLoadHonoursExplicitConfigInClientMode proves client mode still honours an
// EXPLICIT config pointer — the --config argument and the GOFER_CONFIG env — so an
// operator who names a file keeps the normal load/validate path.
func TestLoadHonoursExplicitConfigInClientMode(t *testing.T) {
	t.Setenv(EnvConfigDir, t.TempDir())
	t.Setenv(EnvRunMode, RunModeClient)
	t.Setenv(EnvConfigPath, "")

	t.Run("explicit argument", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "explicit.yaml")
		write(t, path, clientModeConfigYAML("from-flag"))

		cfg, got, err := Load(path)
		if err != nil {
			t.Fatalf("Load(--config) in client mode: %v", err)
		}
		if !samePath(t, got, path) {
			t.Fatalf("resolved path = %q, want %q", got, path)
		}
		if _, ok := cfg.Projects["from-flag"]; !ok {
			t.Fatalf("explicit config was not loaded, projects = %v", cfg.Projects)
		}
	})

	t.Run("GOFER_CONFIG env", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "env.yaml")
		write(t, path, clientModeConfigYAML("from-env"))
		t.Setenv(EnvConfigPath, path)

		cfg, got, err := Load("")
		if err != nil {
			t.Fatalf("Load(GOFER_CONFIG) in client mode: %v", err)
		}
		if !samePath(t, got, path) {
			t.Fatalf("resolved path = %q, want %q", got, path)
		}
		if _, ok := cfg.Projects["from-env"]; !ok {
			t.Fatalf("GOFER_CONFIG config was not loaded, projects = %v", cfg.Projects)
		}
	})
}
