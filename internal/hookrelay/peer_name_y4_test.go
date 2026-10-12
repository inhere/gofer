package hookrelay

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeSessionFile(t *testing.T, dir, file string, v any) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	if err := os.WriteFile(filepath.Join(dir, file), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The session name is read from $CLAUDE_CONFIG_DIR/sessions (default ~/.claude),
// matched by sessionId, with nameSource, and follows a rename on the next beat.
func TestPeerIdentityHonoursClaudeConfigDirAndRename(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	sessions := filepath.Join(cfg, "sessions")
	writeSessionFile(t, sessions, "1.json", map[string]any{"sessionId": "other", "name": "not-me"})
	writeSessionFile(t, sessions, "2.json", map[string]any{"sessionId": "sid-name", "name": "my-tools-dev-22", "nameSource": "user", "status": "idle", "cwd": "/x", "kind": "interactive"})
	if err := os.WriteFile(filepath.Join(sessions, "3.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := newFake()
	if _, err := Run(f, Payload{Agent: AgentClaude, Event: "SessionStart", SessionID: "sid-name", Cwd: cfg}, Options{}); err != nil {
		t.Fatal(err)
	}
	got := f.sessions["sid-name"]
	if got.PeerName != "my-tools-dev-22" || got.PeerNameSource != "user" {
		t.Fatalf("registered peer = %+v", got)
	}

	// the user renames the session: the next heartbeat carries the new name
	writeSessionFile(t, sessions, "2.json", map[string]any{"sessionId": "sid-name", "name": "renamed", "nameSource": "user"})
	if _, err := Run(f, Payload{Agent: AgentClaude, Event: "UserPromptSubmit", SessionID: "sid-name", Prompt: "hi"}, Options{}); err != nil {
		t.Fatal(err)
	}
	last := f.beats[len(f.beats)-1]
	if last.PeerName != "renamed" || last.PeerNameSource != "user" {
		t.Fatalf("heartbeat peer = %+v", last)
	}
}

func TestReadPeerIdentityEdgeCases(t *testing.T) {
	dir := t.TempDir()
	writeSessionFile(t, dir, "1.json", map[string]any{"sessionId": "a", "name": "  spaced  ", "nameSource": "auto"})
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("[1,2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if id, _ := readPeerIdentity("a", dir); id.Name != "spaced" || id.NameSource != "auto" {
		t.Fatalf("identity = %+v", id)
	}
	if id, why := readPeerIdentity("nobody", dir); id != (peerIdentity{}) || why == "" {
		t.Fatalf("no match: %+v %q", id, why)
	}
	if id, why := readPeerIdentity("a", filepath.Join(dir, "missing")); id != (peerIdentity{}) || why == "" {
		t.Fatalf("missing dir: %+v %q", id, why)
	}
	// unknown future field types must not break parsing of the others
	writeSessionFile(t, dir, "2.json", map[string]any{"sessionId": "b", "name": 42})
	if id, _ := readPeerIdentity("b", dir); id.Name != "" {
		t.Fatalf("type-mismatched name leaked: %+v", id)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if d, ok := claudeConfigDir(os.Getenv); ok && filepath.Base(d) != ".claude" {
		t.Fatalf("default dir = %q", d)
	}
}

// A directory full of files left by dead processes must not hide the live session:
// the scan cap applies to the newest files, whatever their names sort as.
func TestReadPeerIdentityFindsNewestBeyondTheScanCap(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	for i := 0; i < peerScanMaxFiles+20; i++ {
		name := fmt.Sprintf("%07d.json", i)
		writeSessionFile(t, dir, name, map[string]any{"sessionId": fmt.Sprintf("dead-%d", i)})
		if err := os.Chtimes(filepath.Join(dir, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	// Sorts after every stale name, and is the most recently written file.
	writeSessionFile(t, dir, "9999999.json", map[string]any{"sessionId": "live", "name": "proj-43", "messagingSocketPath": "/tmp/s.sock"})
	if err := os.WriteFile(filepath.Join(dir, "9999999.abc.key"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if id, why := readPeerIdentity("live", dir); id.Name != "proj-43" || !id.Messaging {
		t.Fatalf("live session not found: %+v %q", id, why)
	}
}
