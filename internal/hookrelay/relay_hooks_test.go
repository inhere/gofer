package hookrelay

import (
	"os"
	"path/filepath"
	"testing"
)

// relayHookJSON is a gofer-owned SessionStart entry in a Claude Code settings file.
const relayHookJSON = `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"gofer hook claude"}]}]}}`

// fakeHome points the user's home at an empty temp dir, so the user-level check never reads the
// settings of the machine running the suite.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestFindRelayHooksLooksAtBothLevels: the SessionStart hook fires from the project settings and
// from the user-level ones alike, so the report must name whichever one carries gofer's entry.
func TestFindRelayHooksLooksAtBothLevels(t *testing.T) {
	home := fakeHome(t)
	project, other := t.TempDir(), t.TempDir()

	if level, known := FindRelayHooks(AgentClaude, project); level != RelayHooksNone || !known {
		t.Fatalf("no hooks anywhere: level=%q known=%v", level, known)
	}
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), relayHookJSON)
	if level, _ := FindRelayHooks(AgentClaude, project); level != RelayHooksUser {
		t.Fatalf("user-level hooks: level=%q, want %q", level, RelayHooksUser)
	}
	writeFile(t, filepath.Join(other, ".claude", "settings.json"), relayHookJSON)
	if level, _ := FindRelayHooks(AgentClaude, other); level != RelayHooksProject {
		t.Fatalf("project-level hooks: level=%q, want %q", level, RelayHooksProject)
	}

	// An agent gofer has no hook template for cannot be reported at all.
	if _, known := FindRelayHooks("no-such-agent", project); known {
		t.Fatal("an agent without a hook template must report known=false")
	}
}
