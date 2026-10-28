package agent

import "testing"

func TestInstallInfoFor(t *testing.T) {
	for _, tc := range []struct{ cli, os, command string }{
		{"claude", "linux", "curl -fsSL https://claude.ai/install.sh | bash"},
		{"claude", "darwin", "curl -fsSL https://claude.ai/install.sh | bash"},
		{"claude", "windows", "irm https://claude.ai/install.ps1 | iex"},
		{"omp", "windows", "irm https://omp.sh/install.ps1 | iex"},
		{"omp", "linux", "curl -fsSL https://omp.sh/install | sh"},
		{"codex", "darwin", "npm install -g @openai/codex"},
		{"opencode", "windows", "npm install -g opencode-ai"},
		{"gemini", "linux", "npm install -g @google/gemini-cli"},
		{"gemini", "freebsd", ""},
	} {
		t.Run(tc.cli+"/"+tc.os, func(t *testing.T) {
			info, ok := InstallInfoFor(tc.cli, tc.os)
			if !ok || info.Command != tc.command || info.URL == "" {
				t.Fatalf("got %#v, %v; want command %q and source", info, ok, tc.command)
			}
		})
	}
	if _, ok := InstallInfoFor("unknown-cli", "linux"); ok {
		t.Fatal("unknown CLI must not have a hint")
	}
}
