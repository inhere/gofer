package hookrelay

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	peerFileMaxBytes = 64 << 10
	peerScanMaxFiles = 128
)

type peerIdentity struct {
	Name      string
	Status    string
	Messaging bool
}

type claudeSessionFile struct {
	SessionID           string `json:"sessionId"`
	Name                string `json:"name"`
	Status              string `json:"status"`
	MessagingSocketPath string `json:"messagingSocketPath"`
}

// readPeerIdentity is deliberately best-effort: Claude's sessions files are an
// internal format. Parse errors, missing fields and a missing directory are all
// silent to the hook caller; the caller may log the bounded diagnostic at debug
// level without changing hook behaviour.
func readPeerIdentity(sessionID, sessionsDir string) (peerIdentity, string) {
	if strings.TrimSpace(sessionsDir) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return peerIdentity{}, "home directory unavailable"
		}
		sessionsDir = filepath.Join(home, ".claude", "sessions")
	}
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return peerIdentity{}, "sessions directory unavailable"
	}
	seen := 0
	for _, entry := range entries {
		if entry.IsDir() || seen >= peerScanMaxFiles {
			continue
		}
		seen++
		f, err := os.Open(filepath.Join(sessionsDir, entry.Name()))
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(f, peerFileMaxBytes+1))
		_ = f.Close()
		if err != nil || len(b) > peerFileMaxBytes {
			continue
		}
		var raw claudeSessionFile
		if json.Unmarshal(b, &raw) != nil || raw.SessionID != sessionID {
			continue
		}
		return peerIdentity{Name: strings.TrimSpace(raw.Name), Status: strings.TrimSpace(raw.Status), Messaging: strings.TrimSpace(raw.MessagingSocketPath) != ""}, ""
	}
	return peerIdentity{}, "matching session file not found"
}
