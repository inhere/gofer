package hookrelay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/client"
)

func TestHookReportsPeerIdentity(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{
		"sessionId": "peer-session-1", "name": "inspect-22", "status": "busy",
		"messagingSocketPath": filepath.Join(dir, "sock"),
	})
	if err := os.WriteFile(filepath.Join(sessions, "123.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	f := newFake()
	result, err := Run(f, Payload{Agent: AgentClaude, Event: "SessionStart", SessionID: "peer-session-1", Cwd: dir}, Options{PeerSessionsDir: sessions})
	if err != nil || result.Blocked {
		t.Fatalf("Run() = %+v, err=%v", result, err)
	}
	if got := f.sessions["peer-session-1"]; got.PeerName != "inspect-22" || got.PeerStatus != "busy" || !got.PeerMessaging {
		t.Fatalf("peer identity = %+v, want name/status/messaging", got)
	}

	missing := newFake()
	if _, err := Run(missing, Payload{Agent: AgentClaude, Event: "SessionStart", SessionID: "missing", Cwd: dir}, Options{PeerSessionsDir: sessions}); err != nil {
		t.Fatal(err)
	}
	if got := missing.sessions["missing"]; got.PeerName != "" || got.PeerStatus != "" || got.PeerMessaging {
		t.Fatalf("missing peer identity = %+v, want silent empty result", got)
	}
}

var _ API = (*fakeAPI)(nil)

var _ client.AgentSession
