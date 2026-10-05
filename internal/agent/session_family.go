package agent

import (
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/internal/config"
)

// A session family groups agents that read and write the SAME on-disk session
// store, so a session id produced by one member can be continued by another
// (e.g. an ACP adapter session continued by the vendor's own CLI). The job
// layer asks this package instead of hard-coding agent names: a resume that
// switches agent (`--agent`) is only allowed inside one family.
//
// Evidence (2026-10-03, read-only inspection of @zed-industries/claude-code-acp
// 0.16.2): claude-acp drives the Claude Agent SDK, which stores sessions at
// $CLAUDE_CONFIG_DIR(default ~/.claude)/projects/<encoded cwd>/<id>.jsonl, the
// very store `claude --resume <id>` reads — so claude-acp and claude
// form one family.
//
// codex-acp joined the codex family on real-host evidence (2026-10-05, codex 0.160 +
// @agentclientprotocol/codex-acp 2.1.1): the ACP session id equals the rollout id in
// ~/.codex/sessions/.../rollout-<ts>-<id>.jsonl, and the conversation was continued
// in BOTH directions (codex-acp one-shot -> `codex exec resume`, codex exec ->
// codex-acp session/load), each quoting the original first user message verbatim.
var builtinSessionFamilies = map[string]string{
	"claude":     "claude",
	"claude-acp": "claude",
	"codex":      "codex",
	"codex-acp":  "codex",
}

// SessionFamily returns the family an agent belongs to, or "" when it shares
// its session store with no other agent. A CLI agent not listed by key falls
// back to its command's base name (a renamed `claude` wrapper stays in the
// family); an ACP agent is only in a family when listed by key.
func SessionFamily(key string, ac config.AgentConfig) string {
	if f, ok := builtinSessionFamilies[key]; ok {
		return f
	}
	if ac.Type == TypeCLIAgent {
		base := strings.TrimSuffix(strings.ToLower(filepath.Base(ac.Command)), ".exe")
		if f, ok := builtinSessionFamilies[base]; ok && f == base {
			return f
		}
	}
	return ""
}

// SessionCompatible reports whether a session started by agent a can be
// continued by agent b: the same agent always can; different agents only when
// both sit in one session family.
func SessionCompatible(aKey string, a config.AgentConfig, bKey string, b config.AgentConfig) bool {
	if aKey == bKey {
		return true
	}
	fa := SessionFamily(aKey, a)
	return fa != "" && fa == SessionFamily(bKey, b)
}

// ResumeCaps is how an agent's session can be continued; it feeds GET /v1/agents
// so a client can grey out resume forms that would be rejected.
type ResumeCaps struct {
	// SessionResume: a non-interactive CLI resume template exists (`--resume -p`).
	SessionResume bool
	// SessionResumeInteractive: an interactive (pty) resume template exists.
	SessionResumeInteractive bool
	// LoadSession: an ACP agent that can reload a session (session/load).
	LoadSession bool
	// Family is SessionFamily(key, ac).
	Family string
}

// ResumeCapabilities summarises how ac's sessions can be continued.
func ResumeCapabilities(key string, ac config.AgentConfig) ResumeCaps {
	caps := ResumeCaps{Family: SessionFamily(key, ac)}
	if ac.Type == TypeACPAgent {
		caps.LoadSession = ac.ACP.AllowsLoadSession()
		return caps
	}
	caps.SessionResume = len(ac.SessionResume) > 0
	caps.SessionResumeInteractive = len(ac.SessionResumeInteractive) > 0
	return caps
}
