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
	// AgentOmp is the omp (oh-my-pi) coding agent: its hooks are TypeScript
	// extensions, so a thin shim (hooks/omp.gofer-relay.ts) feeds this command
	// the same normalised JSON Claude Code / Codex send.
	AgentOmp = "omp"
	// AgentJcode is the jcode coding agent: shell hooks in config.toml [hooks]
	// that carry their payload in JCODE_HOOK_* env vars (see ParseJcodePayload).
	AgentJcode = "jcode"
)

// ValidAgent reports whether a is a supported hook agent.
func ValidAgent(a string) bool {
	switch a {
	case AgentClaude, AgentCodex, AgentOmp, AgentJcode:
		return true
	}
	return false
}

// ObserveOnly reports whether agent's hooks cannot hold the agent back: jcode
// runs every hook except pre_tool detached (fire-and-forget), so a Stop there
// can neither wait for a web reply nor inject one. Such agents are registered
// and kept visible (state, last message, tool progress) but never relayed.
func ObserveOnly(agent string) bool { return agent == AgentJcode }

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
	// ToolName and ToolOutput are populated for PostToolUse.
	ToolName   string
	ToolOutput string
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
	ToolName             string          `json:"tool_name"`
	ToolOutput           string          `json:"tool_output"`
	ToolResponse         json.RawMessage `json:"tool_response"`
	Output               string          `json:"output"`
	TurnID               json.RawMessage `json:"turn_id"`
}

func rawText(raw string, values ...json.RawMessage) string {
	if strings.TrimSpace(raw) != "" {
		return raw
	}
	for _, value := range values {
		if len(value) == 0 || string(value) == "null" {
			continue
		}
		var text string
		if json.Unmarshal(value, &text) == nil {
			return text
		}
		return string(value)
	}
	return ""
}

// maxStdin caps how much hook stdin is read (the payload is small; a
// transcript is never inlined).
const maxStdin = 4 << 20

// ParsePayload decodes hook stdin for agent. An empty body is an error (the
// hook was invoked outside a CLI); missing session_id is an error too.
func ParsePayload(agent string, r io.Reader) (Payload, error) {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if !ValidAgent(agent) {
		return Payload{}, fmt.Errorf("hookrelay: unsupported agent %q (use: claude | codex | omp | jcode)", agent)
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
		ToolName:   raw.ToolName,
		ToolOutput: rawText(raw.ToolOutput, raw.ToolResponse, json.RawMessage(raw.Output)),
	}
	if strings.TrimSpace(p.SessionID) == "" {
		return Payload{}, fmt.Errorf("hookrelay: stdin has no session_id")
	}
	if p.Event == "" {
		return Payload{}, fmt.Errorf("hookrelay: stdin has no hook_event_name")
	}
	return p, nil
}

// jcodeEvents maps jcode's hook event names onto the Claude-style names the
// runner understands. turn_start carries no prompt text, so it maps to an
// (injected-looking) UserPromptSubmit that only marks the session running.
var jcodeEvents = map[string]string{
	"session_start": "SessionStart",
	"turn_start":    "UserPromptSubmit",
	"turn_end":      "Stop",
	"post_tool":     "PostToolUse",
	"session_end":   "SessionEnd",
}

// ParseJcodePayload builds a Payload from a jcode hook invocation. jcode passes
// the event as env vars (JCODE_HOOK_EVENT / _SESSION_ID / _CWD / _TOOL_NAME /
// _LAST_ASSISTANT_TEXT) plus the same data as one JSON object in
// JCODE_HOOK_PAYLOAD; stdin is empty. The JSON wins, env fills the gaps, and a
// JSON document on stdin is accepted as a last resort (tests, manual runs).
// Events jcode adds later (pre_tool, ...) map to "" and are ignored by Run.
func ParseJcodePayload(getenv func(string) string, stdin io.Reader) (Payload, error) {
	var raw map[string]any
	if s := strings.TrimSpace(getenv("JCODE_HOOK_PAYLOAD")); s != "" {
		if err := json.Unmarshal([]byte(s), &raw); err != nil {
			return Payload{}, fmt.Errorf("hookrelay: decode JCODE_HOOK_PAYLOAD: %w", err)
		}
	} else if stdin != nil {
		data, _ := io.ReadAll(io.LimitReader(stdin, maxStdin))
		if strings.TrimSpace(string(data)) != "" {
			if err := json.Unmarshal(data, &raw); err != nil {
				return Payload{}, fmt.Errorf("hookrelay: decode jcode stdin: %w", err)
			}
		}
	}
	str := func(jsonKey, envKey string) string {
		if v, ok := raw[jsonKey].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
		return getenv(envKey)
	}
	event := str("event", "JCODE_HOOK_EVENT")
	p := Payload{
		Agent:                AgentJcode,
		Event:                jcodeEvents[event],
		SessionID:            str("session_id", "JCODE_HOOK_SESSION_ID"),
		Cwd:                  str("cwd", "JCODE_HOOK_CWD"),
		ToolName:             str("tool_name", "JCODE_HOOK_TOOL_NAME"),
		Source:               str("source", "JCODE_HOOK_SOURCE"),
		LastAssistantMessage: str("last_assistant_text", "JCODE_HOOK_LAST_ASSISTANT_TEXT"),
	}
	if strings.TrimSpace(p.SessionID) == "" {
		return Payload{}, fmt.Errorf("hookrelay: jcode hook has no session id (JCODE_HOOK_SESSION_ID)")
	}
	if event == "" {
		return Payload{}, fmt.Errorf("hookrelay: jcode hook has no event (JCODE_HOOK_EVENT)")
	}
	return p, nil
}
