package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestMcpUseClient checks the E28 D1/D2 mode-decision truth table: client mode
// iff not --standalone AND a server addr (flag/env) is resolved.
func TestMcpUseClient(t *testing.T) {
	cases := []struct {
		name       string
		standalone bool
		serverAddr string
		want       bool
	}{
		{"standalone overrides addr (D2 escape)", true, "x", false},
		{"no addr, no standalone -> standalone", false, "", false},
		{"addr set, no standalone -> client (D1)", false, "x", true},
		{"standalone, no addr -> standalone", true, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpUseClient(tc.standalone, tc.serverAddr); got != tc.want {
				t.Fatalf("mcpUseClient(%v, %q) = %v, want %v",
					tc.standalone, tc.serverAddr, got, tc.want)
			}
		})
	}
}

// TestMcpCmdFlags verifies `mcp` binds the connection + escape-hatch flags
// (--server/--token via bindServerFlags, --standalone via mcpOpts) so client
// mode is reachable without re-binding.
func TestMcpCmdFlags(t *testing.T) {
	c := NewMcpCmd()
	c.Config(c) // apply the Config closure to register flags
	for _, name := range []string{"server", "token", "standalone", "config", "project"} {
		if c.Flags.LookupFlag(name) == nil {
			t.Errorf("mcp command missing --%s flag", name)
		}
	}
}

// TestResolveScopedProject covers the --project three-state truth table (xu64.15):
// empty→operator, explicit key, and the four "auto" precedence sources
// (.gofer.project.yaml key: → ProjectForPath → GOFER_PROJECT → error).
func TestResolveScopedProject(t *testing.T) {
	t.Run("empty flag -> operator", func(t *testing.T) {
		got, err := resolveScopedProject("", nil, "/anywhere")
		if err != nil || got != "" {
			t.Fatalf("empty: got (%q,%v), want (\"\",nil)", got, err)
		}
	})

	t.Run("explicit key passthrough", func(t *testing.T) {
		got, err := resolveScopedProject("demo", nil, "/anywhere")
		if err != nil || got != "demo" {
			t.Fatalf("explicit: got (%q,%v), want (demo,nil)", got, err)
		}
	})

	t.Run("auto: overlay key: wins", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectOverlay(t, dir, "key: fromfile\n")
		// GOFER_PROJECT set to a decoy — the overlay key: must win (higher precedence).
		t.Setenv("GOFER_PROJECT", "fromenv")
		cfg := &config.Config{Projects: map[string]config.ProjectConfig{"frompath": {HostPath: dir}}}
		got, err := resolveScopedProject("auto", cfg, dir)
		if err != nil || got != "fromfile" {
			t.Fatalf("auto+overlay: got (%q,%v), want (fromfile,nil)", got, err)
		}
	})

	t.Run("auto: ProjectForPath when no overlay key", func(t *testing.T) {
		dir := t.TempDir() // no overlay file
		t.Setenv("GOFER_PROJECT", "")
		cfg := &config.Config{Projects: map[string]config.ProjectConfig{"frompath": {HostPath: dir}}}
		got, err := resolveScopedProject("auto", cfg, dir)
		if err != nil || got != "frompath" {
			t.Fatalf("auto+path: got (%q,%v), want (frompath,nil)", got, err)
		}
	})

	t.Run("auto: GOFER_PROJECT env fallback (client, cfg nil)", func(t *testing.T) {
		dir := t.TempDir() // no overlay file
		t.Setenv("GOFER_PROJECT", "fromenv")
		got, err := resolveScopedProject("auto", nil, dir)
		if err != nil || got != "fromenv" {
			t.Fatalf("auto+env: got (%q,%v), want (fromenv,nil)", got, err)
		}
	})

	t.Run("auto: nothing resolvable -> error", func(t *testing.T) {
		dir := t.TempDir() // no overlay file
		t.Setenv("GOFER_PROJECT", "")
		if got, err := resolveScopedProject("auto", nil, dir); err == nil {
			t.Fatalf("auto+none: got (%q,nil), want error", got)
		}
	})
}

func writeProjectOverlay(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, config.ProjectOverlayName), []byte(content), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
}
