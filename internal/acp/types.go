package acp

import "encoding/json"

// ProtocolVersion is the ACP protocol version this client speaks (an integer in
// the wire protocol, per the design's protocol memo).
const ProtocolVersion = 1

// JSON-RPC method names exchanged with the agent.
const (
	MethodInitialize        = "initialize"
	MethodSessionNew        = "session/new"
	MethodSessionLoad       = "session/load"
	MethodSessionSetMode    = "session/set_mode"
	MethodSessionPrompt     = "session/prompt"
	MethodSessionCancel     = "session/cancel"
	MethodSessionUpdate     = "session/update"
	MethodRequestPermission = "session/request_permission"
)

// session/prompt stopReason values.
const (
	StopEndTurn         = "end_turn"
	StopMaxTokens       = "max_tokens"
	StopMaxTurnRequests = "max_turn_requests"
	StopRefusal         = "refusal"
	StopCancelled       = "cancelled"
)

// session/update discriminators (Update.Kind).
const (
	UpdateUserMessageChunk  = "user_message_chunk"
	UpdateAgentMessageChunk = "agent_message_chunk"
	UpdateAgentThoughtChunk = "agent_thought_chunk"
	UpdateToolCall          = "tool_call"
	UpdateToolCallUpdate    = "tool_call_update"
	UpdatePlan              = "plan"
	UpdateAvailableCommands = "available_commands_update"
	UpdateCurrentMode       = "current_mode_update"
	// UpdateUsage is the token/cost accounting update an agent reports as it runs
	// (SUP-01 E). The spec leaves its payload to the agent (no typed fields here):
	// the raw object is what the runner reads its recognisable subset from.
	UpdateUsage = "usage_update"
)

// toolCall.kind values — the ACP ToolKind vocabulary. A gofer policy that names
// tool kinds (the project approval gate) is validated against ToolKinds, so the
// vocabulary has exactly one home.
const (
	ToolKindRead       = "read"
	ToolKindEdit       = "edit"
	ToolKindDelete     = "delete"
	ToolKindMove       = "move"
	ToolKindSearch     = "search"
	ToolKindExecute    = "execute"
	ToolKindThink      = "think"
	ToolKindFetch      = "fetch"
	ToolKindSwitchMode = "switch_mode"
	ToolKindOther      = "other"
)

// ToolKinds is the complete ToolKind vocabulary, in schema order.
var ToolKinds = []string{
	ToolKindRead, ToolKindEdit, ToolKindDelete, ToolKindMove, ToolKindSearch,
	ToolKindExecute, ToolKindThink, ToolKindFetch, ToolKindSwitchMode, ToolKindOther,
}

// session/request_permission option kinds.
const (
	OptionAllowOnce    = "allow_once"
	OptionAllowAlways  = "allow_always"
	OptionRejectOnce   = "reject_once"
	OptionRejectAlways = "reject_always"
)

// PermissionOutcome.Outcome values.
const (
	OutcomeSelected  = "selected"
	OutcomeCancelled = "cancelled"
)

// Implementation identifies a client or agent (clientInfo / agentInfo).
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// ClientCapabilities is what the client offers the agent. S0 declares NO fs and NO
// terminal delegation: the agent uses its own tools, so the fields stay nil/false
// and the agent's fs/*, terminal/* requests are refused with -32601.
type ClientCapabilities struct {
	FS       *FSCapabilities `json:"fs,omitempty"`
	Terminal bool            `json:"terminal"`
}

// FSCapabilities is the client-side file-system delegation block. Never set in S0.
type FSCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

// InitializeParams is the initialize request.
type InitializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
	ClientInfo         *Implementation    `json:"clientInfo,omitempty"`
}

// AgentCapabilities is the agent's side of the capability negotiation. The
// prompt/mcp capability blocks are carried verbatim (S0 only reads LoadSession).
type AgentCapabilities struct {
	LoadSession        bool            `json:"loadSession,omitempty"`
	PromptCapabilities json.RawMessage `json:"promptCapabilities,omitempty"`
	MCPCapabilities    json.RawMessage `json:"mcpCapabilities,omitempty"`
}

// InitializeResult is the initialize response.
type InitializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AgentInfo         *Implementation   `json:"agentInfo,omitempty"`
	AuthMethods       json.RawMessage   `json:"authMethods,omitempty"`
}

// EnvVariable is one name/value pair of an MCP server's environment.
type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// MCPServer is one entry of session/new's mcpServers (stdio transport).
type MCPServer struct {
	Name    string        `json:"name"`
	Command string        `json:"command"`
	Args    []string      `json:"args,omitempty"`
	Env     []EnvVariable `json:"env,omitempty"`
}

// SessionNewParams is the session/new request.
type SessionNewParams struct {
	Cwd        string      `json:"cwd"`
	MCPServers []MCPServer `json:"mcpServers"`
}

// SessionLoadParams is the session/load request (resume; S2).
type SessionLoadParams struct {
	SessionID  string      `json:"sessionId"`
	Cwd        string      `json:"cwd"`
	MCPServers []MCPServer `json:"mcpServers"`
}

// SessionSetModeParams is the session/set_mode request.
type SessionSetModeParams struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

// SessionMode is one selectable agent mode (e.g. read-only).
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// SessionModes is the mode block a session/new response may carry.
type SessionModes struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
}

// SessionNewResult is the session/new (and session/load) response.
type SessionNewResult struct {
	SessionID string        `json:"sessionId"`
	Modes     *SessionModes `json:"modes,omitempty"`
}

// ContentBlock is one prompt/update content block. S0 only sends and consumes
// text blocks; other block types are carried by Raw where they matter.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// PromptParams is the session/prompt request.
type PromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// PromptResult is the session/prompt response.
type PromptResult struct {
	StopReason string `json:"stopReason"`
}

// SessionCancelParams is the session/cancel notification (a notification, not a
// request: the agent answers by ending the turn with stopReason cancelled).
type SessionCancelParams struct {
	SessionID string `json:"sessionId"`
}

// SessionUpdateParams is the session/update notification.
type SessionUpdateParams struct {
	SessionID string `json:"sessionId"`
	Update    Update `json:"update"`
}

// ToolCallLocation is a file location a tool call touched.
type ToolCallLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

// ToolCall is ACP's tool_call payload. session/update carries it twice: kind
// tool_call (the initial call, with title/kind/rawInput) and kind tool_call_update
// (a status/content refresh, every field optional except toolCallId). One struct
// decodes both — Update.Kind tells which one arrived.
type ToolCall struct {
	ToolCallID string             `json:"toolCallId"`
	Title      string             `json:"title,omitempty"`
	Kind       string             `json:"kind,omitempty"`
	Status     string             `json:"status,omitempty"`
	Content    json.RawMessage    `json:"content,omitempty"`
	Locations  []ToolCallLocation `json:"locations,omitempty"`
	RawInput   json.RawMessage    `json:"rawInput,omitempty"`
	RawOutput  json.RawMessage    `json:"rawOutput,omitempty"`
}

// PlanEntry is one item of an agent plan.
type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status,omitempty"`
}

// Plan is the session/update plan payload.
type Plan struct {
	Entries []PlanEntry `json:"entries"`
}

// AvailableCommand is one entry of available_commands_update.
type AvailableCommand struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
}

// Update is one decoded session/update payload. Kind is the `sessionUpdate`
// discriminator; Raw is the untouched update object, so a variant S0 does not
// model (or a future field) is still available to the caller. Exactly one typed
// payload is set for the kinds this client understands.
type Update struct {
	Kind              string
	Raw               json.RawMessage
	MessageChunk      *ContentBlock // agent_message_chunk / agent_thought_chunk / user_message_chunk
	ToolCall          *ToolCall     // tool_call and tool_call_update
	Plan              *Plan         // plan
	CurrentModeID     string        // current_mode_update
	AvailableCommands []AvailableCommand
}

// UnmarshalJSON decodes the discriminated payload. The variant fields differ per
// kind (a message chunk's `content` is a block, a tool call's `content` is a list
// of tool-call content), which is why the split lives here rather than in a flat
// struct.
func (u *Update) UnmarshalJSON(b []byte) error {
	var head struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return err
	}
	u.Kind = head.SessionUpdate
	u.Raw = append(json.RawMessage(nil), b...)
	switch head.SessionUpdate {
	case UpdateUserMessageChunk, UpdateAgentMessageChunk, UpdateAgentThoughtChunk:
		var v struct {
			Content ContentBlock `json:"content"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.MessageChunk = &v.Content
	case UpdateToolCall, UpdateToolCallUpdate:
		var v ToolCall
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.ToolCall = &v
	case UpdatePlan:
		var v Plan
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.Plan = &v
	case UpdateCurrentMode:
		var v struct {
			CurrentModeID string `json:"currentModeId"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.CurrentModeID = v.CurrentModeID
	case UpdateAvailableCommands:
		var v struct {
			AvailableCommands []AvailableCommand `json:"availableCommands"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		u.AvailableCommands = v.AvailableCommands
	}
	return nil
}

// PermissionOption is one choice offered by the agent's permission request.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name,omitempty"`
	Kind     string `json:"kind"`
}

// RequestPermissionParams is the agent→client session/request_permission request.
type RequestPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  *ToolCall          `json:"toolCall,omitempty"`
	Options   []PermissionOption `json:"options"`
}

// PermissionOutcome is the answer to a permission request: either a selected
// option or a cancellation. It marshals as the ACP `outcome` object.
type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// PermissionSelected answers a permission request with one of the offered options.
func PermissionSelected(optionID string) PermissionOutcome {
	return PermissionOutcome{Outcome: OutcomeSelected, OptionID: optionID}
}

// PermissionCancelled answers a permission request without choosing an option
// (the agent then aborts the tool call / the turn).
func PermissionCancelled() PermissionOutcome {
	return PermissionOutcome{Outcome: OutcomeCancelled}
}

// permissionResult is the session/request_permission response envelope.
type permissionResult struct {
	Outcome PermissionOutcome `json:"outcome"`
}
