// Package hooks embeds the agent-CLI hook templates `gofer init hooks` merges
// into a project's (or the user's) Claude Code / Codex hook configuration —
// one source of truth, no drift between the tracked hooks/ tree and what init
// writes out (same rationale as skills/embed.go). Every hook entry runs the
// gofer binary itself (`gofer hook <agent>`), so there is no script to ship.
package hooks

import _ "embed"

// ClaudeSettings is the hooks fragment merged into .claude/settings.json
// (hooks.* arrays + env.CLAUDE_CODE_STOP_HOOK_BLOCK_CAP).
//
//go:embed claude.settings.json
var ClaudeSettings []byte

// CodexHooks is the hooks fragment merged into .codex/hooks.json.
//
//go:embed codex.hooks.json
var CodexHooks []byte
