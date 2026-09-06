// Package hookrelay is the agent-CLI hook executor behind `gofer hook
// <agent>` (session relay, SESS-01 D1/D5). It normalises the hook stdin JSON
// of Claude Code and Codex into one Payload, reports session events to the hub
// and — on Stop while the session's relay switch is on — posts the agent's last
// message as a turn, blocks for the human's web reply and emits the
// `{"decision":"block","reason":...}` continuation that injects the reply into
// the same terminal session.
//
// It also owns the hook-config merge `gofer init hooks` performs (install.go).
// The package sits beside internal/client (it consumes it) and below
// internal/commands (which only binds flags and calls Run) — G021/G022.
package hookrelay

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Supported agents (the `gofer hook <agent>` argument).
const (
	AgentClaude = "claude"
	AgentCodex  = "codex"
)

// ValidAgent reports whether a is a supported hook agent.
func ValidAgent(a string) bool {
	return a == AgentClaude || a == AgentCodex
}

// Payload is the agent-neutral view of one hook invocation's stdin.
type Payload struct {
	Agent          string
	Event          string // hook_event_name
	SessionID      string
	Cwd            string
	TranscriptPath string
	StopHookActive bool
	// LastAssistantMessage is set directly by Codex on Stop; for Claude it is
	// read from the transcript by the runner (LastAssistantText).
	LastAssistantMessage string
	// Prompt is the user's text on UserPromptSubmit (both agents).
	Prompt string
	// NotificationType / Message are the Claude Notification fields.
	NotificationType string
	Message          string
	// Source is the Codex SessionStart source (startup|resume|clear|compact).
	Source string
}

// rawPayload lists every stdin field either agent may send; unknown keys are
// ignored so a newer CLI never breaks the hook.
type rawPayload struct {
	SessionID            string          `json:"session_id"`
	Cwd                  string          `json:"cwd"`
	TranscriptPath       string          `json:"transcript_path"`
	HookEventName        string          `json:"hook_event_name"`
	StopHookActive       bool            `json:"stop_hook_active"`
	LastAssistantMessage string          `json:"last_assistant_message"`
	Prompt               string          `json:"prompt"`
	NotificationType     string          `json:"notification_type"`
	Message              string          `json:"message"`
	Source               string          `json:"source"`
	TurnID               json.RawMessage `json:"turn_id"`
}

// maxStdin caps how much hook stdin is read (the payload is small; a
// transcript is never inlined).
const maxStdin = 4 << 20

// ParsePayload decodes hook stdin for agent. An empty body is an error (the
// hook was invoked outside a CLI); missing session_id is an error too.
func ParsePayload(agent string, r io.Reader) (Payload, error) {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if !ValidAgent(agent) {
		return Payload{}, fmt.Errorf("hookrelay: unsupported agent %q (use: claude | codex)", agent)
	}
	data, err := io.ReadAll(io.LimitReader(r, maxStdin))
	if err != nil {
		return Payload{}, fmt.Errorf("hookrelay: read stdin: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return Payload{}, fmt.Errorf("hookrelay: empty stdin (run only as a %s hook)", agent)
	}
	var raw rawPayload
	if err := json.Unmarshal(data, &raw); err != nil {
		return Payload{}, fmt.Errorf("hookrelay: decode stdin: %w", err)
	}
	p := Payload{
		Agent: agent, Event: raw.HookEventName, SessionID: raw.SessionID, Cwd: raw.Cwd,
		TranscriptPath: raw.TranscriptPath, StopHookActive: raw.StopHookActive,
		LastAssistantMessage: raw.LastAssistantMessage, Prompt: raw.Prompt,
		NotificationType: raw.NotificationType, Message: raw.Message, Source: raw.Source,
	}
	if strings.TrimSpace(p.SessionID) == "" {
		return Payload{}, fmt.Errorf("hookrelay: stdin has no session_id")
	}
	if p.Event == "" {
		return Payload{}, fmt.Errorf("hookrelay: stdin has no hook_event_name")
	}
	return p, nil
}
