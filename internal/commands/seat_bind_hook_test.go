package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seatHome points the user's home at an empty temp dir: the user-level hook check then reads a
// file the test writes instead of the real ~/.claude/settings.json of this machine.
func seatHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// writeSeatRelayHook writes a gofer-owned SessionStart entry into a Claude Code settings file.
func writeSeatRelayHook(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"gofer hook claude"}]}]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// seatBindTerminal is one `seat bind` run against a terminal seat of project p1 (whose host path
// /work/p1 carries no hooks).
func seatBindTerminal(t *testing.T) string {
	t.Helper()
	stub := &seatStub{
		seats:    []map[string]any{{"id": "seat-1", "name": "desk", "project_key": "p1", "kind": "terminal", "state": "none", "charter_by": "user"}},
		sessions: []map[string]any{{"session_id": "sid-current", "agent": "claude", "state": "idle", "cwd": "/work/p1"}},
	}
	seatTestStub(t, stub)
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "bind", "desk@p1"})
	})
	if code != 0 {
		t.Fatalf("bind exit=%d out=%s", code, out)
	}
	return out
}

// TestSeatBindFindsAUserLevelRelayHook: a relay hook installed once for the user is in effect
// everywhere, so binding in a project whose own settings carry none must not warn.
func TestSeatBindFindsAUserLevelRelayHook(t *testing.T) {
	home := seatHome(t)
	writeSeatRelayHook(t, filepath.Join(home, ".claude", "settings.json"))
	out := seatBindTerminal(t)
	if strings.Contains(out, "WARNING") {
		t.Fatalf("a user-level relay hook is in effect, no warning is due: %s", out)
	}
	if !strings.Contains(out, "user level") {
		t.Fatalf("bind must say where the hook was found: %s", out)
	}
}

// TestSeatBindWarnsWithoutAnyRelayHook: with no hook at either level the warning stays — the
// charter really would not reach the session.
func TestSeatBindWarnsWithoutAnyRelayHook(t *testing.T) {
	seatHome(t)
	if out := seatBindTerminal(t); !strings.Contains(out, "WARNING") {
		t.Fatalf("no relay hook anywhere must warn: %s", out)
	}
}

// TestSeatBindACPSeatSkipsTheHookHint: an acp seat is opened by a gofer session job, whose prompt
// carries the seat block; it needs no terminal relay hook, so nothing about hooks is printed.
func TestSeatBindACPSeatSkipsTheHookHint(t *testing.T) {
	seatHome(t)
	stub := &seatStub{
		seats:    []map[string]any{{"id": "seat-1", "name": "weekly", "project_key": "p1", "kind": "acp", "state": "none", "charter_by": "user"}},
		sessions: []map[string]any{{"session_id": "sid-current", "agent": "claude", "state": "idle", "cwd": "/work/p1"}},
	}
	seatTestStub(t, stub)
	// gcli binds the flags into the package globals; leave them as this test found them.
	prev := seatBindOpts.ref
	t.Cleanup(func() { seatBindOpts.ref = prev })
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "bind", "weekly@p1", "--ref", "job-20261119-abcdef"})
	})
	if code != 0 {
		t.Fatalf("bind exit=%d out=%s", code, out)
	}
	if strings.Contains(out, "relay hook") || strings.Contains(out, "WARNING") {
		t.Fatalf("an acp seat needs no relay hook: %s", out)
	}
}
