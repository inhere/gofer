package hookrelay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The hook reports Claude Code's own messaging endpoint (design 2026-11-05): the
// register carries it once, and every beat carries the CURRENT value — so an address
// Claude Code dropped is cleared instead of kept alive by a stale row. The value is
// trimmed but never judged here: the server owns the whitelist.
func TestHookReportsPeerSocket(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	const socket = "/run/user/1000/cc-socks/4242.sock"
	data, _ := json.Marshal(map[string]any{
		"sessionId": "peer-socket-1", "name": "inspect-22", "messagingSocketPath": "  " + socket + "  ",
	})
	if err := os.WriteFile(filepath.Join(sessions, "123.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	f := newFake()
	if _, err := Run(f, Payload{Agent: AgentClaude, Event: "SessionStart", SessionID: "peer-socket-1", Cwd: dir},
		Options{PeerSessionsDir: sessions}); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRegister.PeerSocket; got != socket {
		t.Fatalf("register peer_socket = %q, want %q", got, socket)
	}

	if _, err := Run(f, Payload{Agent: AgentClaude, Event: "Notification", SessionID: "peer-socket-1", Cwd: dir},
		Options{PeerSessionsDir: sessions}); err != nil {
		t.Fatal(err)
	}
	last := f.beats[len(f.beats)-1]
	if last.PeerSocket == nil || *last.PeerSocket != socket {
		t.Fatalf("beat peer_socket = %v, want %q", last.PeerSocket, socket)
	}

	// No session file: the beat still says "no address" (a pointer to ""), which is
	// what clears a stored one.
	if _, err := Run(f, Payload{Agent: AgentClaude, Event: "Notification", SessionID: "peer-socket-1", Cwd: dir},
		Options{PeerSessionsDir: filepath.Join(dir, "missing")}); err != nil {
		t.Fatal(err)
	}
	last = f.beats[len(f.beats)-1]
	if last.PeerSocket == nil || *last.PeerSocket != "" {
		t.Fatalf("beat without a session file: peer_socket = %v, want a pointer to \"\"", last.PeerSocket)
	}
}
