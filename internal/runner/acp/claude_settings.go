package acp

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/util"
)

// The claude user settings file F14 reads (design 2026-09-17 §F14): the same file the
// claude CLI itself loads its `env` block from. claude relocates the whole config
// directory with CLAUDE_CONFIG_DIR, so the file is $CLAUDE_CONFIG_DIR/settings.json
// when that variable is set and <home>/.claude/settings.json otherwise.
const (
	claudeConfigDirEnv = "CLAUDE_CONFIG_DIR"
	claudeConfigDir    = ".claude"
	claudeSettingsFile = "settings.json"
)

// claudeSettingsExtra returns the environment an acp-agent should inherit from
// claude's user settings file, or nil when there is nothing to add.
//
// Why this exists: claude's key and relay URL live in that file's `env` block for the
// CLI (which reads the file itself), while gofer's own process environment carries no
// ANTHROPIC_* at all. An adapter that resolves credentials before the session —
// claude-code-acp goes through the Claude Agent SDK — therefore answers "Authentication
// required" (real machine, F14). Inheriting the block keeps the key in ONE place.
//
// The rules, in order:
//
//   - Only the `env` object's STRING values are read; anything else in the file is
//     ignored (settings.json is claude's file, not ours).
//   - A key the current process environment or the job's own env map already defines
//     is skipped, NEVER overridden: those are explicit decisions (agent env, .env,
//     the caller's shell) and the settings file is the fallback, not the authority.
//     Presence, not a non-empty value, is the question — an exported empty variable
//     is still an explicit setting.
//   - SEC-01 wins: a key the job's environment is denied (util.EnvironKeyDenied, with
//     the job's allow list re-admitting) is dropped even though it is not inherited.
//     The settings file must not become a side door for GOFER_TOKEN.
//   - A missing, unreadable, malformed or env-less file is a WARNING and an empty
//     result: the inheritance is a convenience, never a reason to fail a job.
//
// The values go into the child's environment and nowhere else. Only the KEY names are
// logged (one Info line naming how many were added) — a value must never reach
// request_json, the rendered command, an event, a job log or gofer's own log.
func claudeSettingsExtra(jobID string, jobEnv map[string]string, deny, allow []string) map[string]string {
	path, err := claudeSettingsPath(jobEnv)
	if err != nil {
		// Fail CLOSED: with no config dir and no home directory there is no path to
		// read, and guessing (or reading a path relative to the serve process's cwd,
		// which may be any project's checkout) would inherit somebody else's file.
		slog.Warn("acp runner: cannot resolve claude's settings path", "job_id", jobID, "err", err)
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		// Not being able to read the file is the expected state for an operator who
		// authenticates the agent by env vars, so this is one warning, not an error.
		slog.Warn("acp runner: cannot read claude settings file", "job_id", jobID, "path", path, "err", err)
		return nil
	}
	var doc struct {
		Env map[string]any `json:"env"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		slog.Warn("acp runner: cannot parse claude settings file", "job_id", jobID, "path", path, "err", err)
		return nil
	}
	if len(doc.Env) == 0 {
		slog.Warn("acp runner: claude settings file declares no env block", "job_id", jobID, "path", path)
		return nil
	}
	keys := make([]string, 0, len(doc.Env))
	for key := range doc.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys) // deterministic log line, and a stable merge order for tests
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		value, ok := doc.Env[key].(string)
		if !ok {
			continue
		}
		if envDefines(jobEnv, key) || util.EnvironKeyDenied(key, deny, allow) {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	added := make([]string, 0, len(out))
	for key := range out {
		added = append(added, key)
	}
	sort.Strings(added)
	slog.Info("acp runner: inherited claude settings env",
		"job_id", jobID, "path", path, "count", len(added), "keys", strings.Join(added, ","))
	return out
}

// claudeSettingsPath resolves the settings file this host's claude CLI would read:
// $CLAUDE_CONFIG_DIR/settings.json when that variable is set (the job's own env first,
// then the process environment — the child sees the job's value, so that is the file
// its claude reads), else <home>/.claude/settings.json. It reports an error when
// neither a config dir nor a home directory exists, which the caller treats as "nothing
// to inherit" rather than falling back to a relative path.
func claudeSettingsPath(jobEnv map[string]string) (string, error) {
	if dir := envValue(jobEnv, claudeConfigDirEnv); dir != "" {
		return filepath.Join(dir, claudeSettingsFile), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%s is unset and the home directory is unknown: %w", claudeConfigDirEnv, err)
	}
	if home == "" {
		return "", fmt.Errorf("%s is unset and the home directory is empty", claudeConfigDirEnv)
	}
	return filepath.Join(home, claudeConfigDir, claudeSettingsFile), nil
}

// envDefines reports whether key already has a value in this process's environment or
// in the job's own env map — the "an explicit setting wins" test above. Names are
// matched case-insensitively in the map, like every other env-key comparison in gofer
// (util.EnvironKeyDenied): on Windows they ARE the same variable, and on unix the
// stricter answer only ever means "do not override".
func envDefines(jobEnv map[string]string, key string) bool {
	if _, ok := os.LookupEnv(key); ok {
		return true
	}
	for k := range jobEnv {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// envValue reads one key from the job's env map case-insensitively, so a caller that
// spelled CLAUDE_CONFIG_DIR in another case still relocates the config dir the way the
// child's claude does.
func envValue(jobEnv map[string]string, key string) string {
	for k, v := range jobEnv {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return os.Getenv(key)
}
