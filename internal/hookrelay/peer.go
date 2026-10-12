package hookrelay

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	peerFileMaxBytes = 64 << 10
	peerScanMaxFiles = 128
)

type peerIdentity struct {
	Name       string
	NameSource string
	Status     string
	Messaging  bool
}

// claudeSessionFile is the shape of <claude-config-dir>/sessions/<pid>.json.
// NOTE: this is Claude Code's INTERNAL, undocumented bookkeeping format (also
// carrying cwd / kind / ...); it may change at any release, so every field is
// optional and a mismatch is silently ignored — the web just shows no name.
type claudeSessionFile struct {
	SessionID           string `json:"sessionId"`
	Name                string `json:"name"`
	NameSource          string `json:"nameSource"`
	Status              string `json:"status"`
	MessagingSocketPath string `json:"messagingSocketPath"`
}

// claudeConfigDir is Claude Code's configuration directory: $CLAUDE_CONFIG_DIR
// when set, else ~/.claude.
func claudeConfigDir(getenv func(string) string) (string, bool) {
	if d := strings.TrimSpace(getenv("CLAUDE_CONFIG_DIR")); d != "" {
		return d, true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return filepath.Join(home, ".claude"), true
}

// readPeerIdentity is deliberately best-effort: Claude's sessions files are an
// internal format. Parse errors, missing fields and a missing directory are all
// silent to the hook caller; the caller may log the bounded diagnostic at debug
// level without changing hook behaviour.
func readPeerIdentity(sessionID, sessionsDir string) (peerIdentity, string) {
	if strings.TrimSpace(sessionsDir) == "" {
		dir, ok := claudeConfigDir(os.Getenv)
		if !ok {
			return peerIdentity{}, "home directory unavailable"
		}
		sessionsDir = filepath.Join(dir, "sessions")
	}
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return peerIdentity{}, "sessions directory unavailable"
	}
	// Newest first: the directory keeps the files of long-dead processes (hundreds
	// of them on a busy machine) while a live session keeps rewriting its own, so the
	// scan cap must apply to the most recent files, not the first names in order.
	type candidate struct {
		name string
		mod  int64
	}
	files := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, candidate{entry.Name(), info.ModTime().UnixNano()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod > files[j].mod })
	if len(files) > peerScanMaxFiles {
		files = files[:peerScanMaxFiles]
	}
	for _, c := range files {
		f, err := os.Open(filepath.Join(sessionsDir, c.name))
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
		return peerIdentity{Name: strings.TrimSpace(raw.Name), NameSource: strings.TrimSpace(raw.NameSource), Status: strings.TrimSpace(raw.Status), Messaging: strings.TrimSpace(raw.MessagingSocketPath) != ""}, ""
	}
	return peerIdentity{}, "matching session file not found"
}
