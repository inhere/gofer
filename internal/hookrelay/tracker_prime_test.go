package hookrelay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallTrackerPrimeReplacesAnyBdPrime covers the older bd setups found in
// real repositories: a plain `bd prime` (no --hook-json) on SessionStart and a
// second one on PreCompact. Every bd prime command must go — PreCompact
// included, or bd keeps injecting its "do not commit" rules after compaction —
// empty groups are dropped, other tools' hooks stay, and SessionStart ends up
// with exactly one gofer tracker prime.
func TestInstallTrackerPrimeReplacesAnyBdPrime(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := `{"hooks":{
  "PreCompact":[{"matcher":"","hooks":[{"type":"command","command":"bd prime"}]}],
  "SessionStart":[
    {"matcher":"","hooks":[{"type":"command","command":"bd prime"}]},
    {"matcher":"","hooks":[{"type":"command","command":"gofer hook claude"},{"type":"command","command":"bd prime --hook-json"}]}
  ],
  "Stop":[{"matcher":"","hooks":[{"type":"command","command":"other-tool stop"}]}]
}}`
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := InstallTrackerPrime("claude", root, true)
	if err != nil || !changed {
		t.Fatalf("install: changed=%v err=%v", changed, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "bd prime") {
		t.Fatalf("bd prime left behind:\n%s", raw)
	}
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Hooks["PreCompact"]; ok {
		t.Fatalf("empty PreCompact event should be dropped:\n%s", raw)
	}
	var primes, relay int
	for _, group := range doc.Hooks["SessionStart"] {
		if len(group.Hooks) == 0 {
			t.Fatalf("empty SessionStart group left behind:\n%s", raw)
		}
		for _, h := range group.Hooks {
			switch h.Command {
			case trackerPrimeCommand:
				primes++
			case "gofer hook claude":
				relay++
			}
		}
	}
	if primes != 1 || relay != 1 {
		t.Fatalf("SessionStart primes=%d relay=%d, want 1/1:\n%s", primes, relay, raw)
	}
	if len(doc.Hooks["Stop"]) != 1 {
		t.Fatalf("other tools' hooks must stay:\n%s", raw)
	}
	if changed, err := InstallTrackerPrime("claude", root, true); err != nil || changed {
		t.Fatalf("second install should be a no-op: changed=%v err=%v", changed, err)
	}
}
