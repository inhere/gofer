package hookrelay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrackerPrimeOnlyInstallAndRemoveKeepsRelayHooks(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := `{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"gofer hook claude"},{"type":"command","command":"other start"}]}],"Stop":[{"matcher":"","hooks":[{"type":"command","command":"gofer hook claude --wait 10"}]}]}}`
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := InstallTrackerPrime("claude", root)
	if err != nil || !changed {
		t.Fatalf("prime-only install: changed=%v err=%v", changed, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), trackerPrimeCommandFor("claude")) != 1 || !strings.Contains(string(raw), "gofer hook claude") {
		t.Fatalf("prime-only install changed unrelated hooks:\n%s", raw)
	}
	if changed, err := InstallTrackerPrime("claude", root); err != nil || changed {
		t.Fatalf("repeat prime-only install should be idempotent: changed=%v err=%v", changed, err)
	}
	if changed, err := RemoveTrackerPrime("claude", root); err != nil || !changed {
		t.Fatalf("prime-only remove: changed=%v err=%v", changed, err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), trackerPrimePrefix) || !strings.Contains(string(raw), "gofer hook claude") || !strings.Contains(string(raw), "other start") {
		t.Fatalf("prime-only remove did not preserve relay/foreign hooks:\n%s", raw)
	}
	if changed, err := RemoveTrackerPrime("claude", root); err != nil || changed {
		t.Fatalf("repeat prime-only remove should be idempotent: changed=%v err=%v", changed, err)
	}
}
