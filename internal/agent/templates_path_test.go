package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestClaudeACPAdapterPreference pins R15: the built-in claude-acp template launches
// the adapter binary directly when PATH has it, and keeps the npx fallback when it does
// not. The probe is a real PATH lookup, so the case builds a fake PATH in a temp dir.
func TestClaudeACPAdapterPreference(t *testing.T) {
	// stubPATH writes the given command names into a fresh dir and points PATH at it.
	// Windows resolves via PATHEXT, unix needs the bare name — write both spellings.
	stubPATH := func(t *testing.T, names ...string) {
		t.Helper()
		dir := t.TempDir()
		for _, name := range names {
			for _, file := range []string{name, name + ".bat"} {
				if err := os.WriteFile(filepath.Join(dir, file), []byte("@echo off\nexit /b 0\n"), 0o755); err != nil {
					t.Fatalf("write stub %s: %v", file, err)
				}
			}
		}
		t.Setenv("PATH", dir)
		t.Setenv("PATHEXT", ".BAT;.EXE")
	}

	cases := []struct {
		name  string
		path  []string
		want  string
		args  []string
		onPATH bool
	}{
		{name: "adapter on PATH wins", path: []string{"npx", claudeAgentACPAdapter},
			want: claudeAgentACPAdapter, onPATH: true},
		{name: "npx fallback without adapter", path: []string{"npx"},
			want: "npx", args: []string{"-y", "@agentclientprotocol/claude-agent-acp"}, onPATH: true},
		{name: "neither: not injected", path: []string{"claude"}, onPATH: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubPATH(t, tc.path...)
			cfg := &config.Config{Agents: map[string]config.AgentConfig{}}
			cfg, _ = Resolve(cfg, DefaultDetector())

			got, ok := cfg.Agents["claude-acp"]
			if ok != tc.onPATH {
				t.Fatalf("claude-acp declared = %v, want %v (PATH=%v)", ok, tc.onPATH, tc.path)
			}
			if !tc.onPATH {
				return
			}
			if got.Command != tc.want {
				t.Fatalf("command = %q, want %q", got.Command, tc.want)
			}
			if !reflect.DeepEqual(got.Args, tc.args) {
				t.Fatalf("args = %#v, want %#v", got.Args, tc.args)
			}
			if !cfg.InjectedAgents()["claude-acp"] {
				t.Fatal("claude-acp is not marked injected (G044)")
			}
			// The ACP block is the adapter's, not the launcher's: it must survive the switch.
			if got.ACP == nil || got.ACP.Modes["read_only"] != "plan" || got.ACP.ClaudeSettingsEnv == nil || !*got.ACP.ClaudeSettingsEnv {
				t.Fatalf("ACP block changed with the launcher: %+v", got.ACP)
			}
			// The adapter's own binary is its version probe; npx needed the explicit one.
			want := config.DetectConfig{Command: claudeAgentACPAdapter, Args: []string{"--version"}}
			if tc.want == claudeAgentACPAdapter && !reflect.DeepEqual(got.Detect, want) {
				t.Fatalf("detect = %+v, want %+v", got.Detect, want)
			}
		})
	}
}
