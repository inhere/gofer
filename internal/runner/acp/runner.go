// Package acp runs an `acp-agent` job: it starts the agent's ACP server over stdio,
// drives exactly one prompt turn (initialize → session/new → session/prompt) and
// maps the protocol's events onto the job's outputs — pure agent text to
// stdout.log, the structured event stream to <result_dir>/artifacts/acp.jsonl, and
// tool-call status changes to job events.
//
// See docs/design/2026-09-17-acp-agent-and-approval-gate-design.md §一 (S0). The
// protocol client itself lives in internal/acp; this package is the mapping layer.
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/inhere/gofer/internal/acp"
	"github.com/inhere/gofer/internal/runner"
)

// Name is the runner identifier ("acp"). It is NOT a configurable runner key: the
// job service selects it for acp-agent jobs the way it selects the pty runner for
// interactive ones.
const Name = "acp"

// ACPFileName is the structured event stream this runner writes, relative to the
// job result dir. It sits under artifacts/ so it is part of the job's artifact
// manifest (listable + downloadable like any other artifact).
const ACPFileName = "acp.jsonl"

// maxEventLineBytes caps one acp.jsonl line. Raw payloads are truncated well below
// this (maxRawBytes); the cap is a final guard for a pathological plan/update.
const maxEventLineBytes = 4096

// maxRawBytes caps an embedded rawInput/rawOutput blob in acp.jsonl.
const maxRawBytes = 512

// Runner executes acp-agent jobs.
type Runner struct{}

// New returns an acp runner.
func New() *Runner { return &Runner{} }

// Name implements runner.Runner.
func (r *Runner) Name() string { return Name }

// Run drives one prompt turn against the agent in req.Command/req.Args.
//
// Outcome mapping (design §一.3): end_turn → exit 0; max_tokens/max_turn_requests →
// exit 0 with a warning and the stop_reason recorded; refusal → non-zero with an
// error; cancelled → the job's context error (the job service classifies it as
// cancelled or timeout); a protocol/transport failure → non-zero with that error.
// The session id and stopReason ride the result so the job row records both.
func (r *Runner) Run(ctx context.Context, req runner.Request) runner.Result {
	if req.ACP == nil {
		return runner.Result{ExitCode: -1, Err: errors.New("acp: runner called without an acp request")}
	}
	switch req.ACP.PermissionPolicy {
	case "", runner.ACPPermissionAutoAllow:
		// S0: auto-allow (or unset, which defaults to it).
	default:
		// Refuse rather than silently auto-approving a policy the operator asked to
		// be stricter: ask/strict are S1.
		return runner.Result{ExitCode: -1, Err: fmt.Errorf("acp: permission_policy %q is not supported yet (S1)", req.ACP.PermissionPolicy)}
	}

	events, err := openEventWriter(req.ACP.ResultDir)
	if err != nil {
		// Best-effort: the event stream is audit/telemetry, the agent's text is the
		// job's product. Log and run without it.
		slog.Warn("acp runner: cannot open event stream", "job_id", req.JobID, "err", err)
	}
	defer func() {
		if cerr := events.Close(); cerr != nil {
			slog.Debug("acp runner: close event stream", "job_id", req.JobID, "err", cerr)
		}
	}()

	h := &handler{stdout: req.Stdout, events: events, onJobEvent: req.OnJobEvent, toolStatus: map[string]string{}}
	client, err := acp.Start(ctx, acp.Options{
		Command: req.Command,
		Args:    req.Args,
		Dir:     req.WorkDir,
		Env:     req.Env,
		Stderr:  req.Stderr,
	})
	if err != nil {
		return runner.Result{ExitCode: -1, Err: err}
	}
	defer func() {
		if cerr := client.Close(); cerr != nil {
			slog.Debug("acp runner: close agent", "job_id", req.JobID, "err", cerr)
		}
	}()

	initRes, err := client.Initialize(ctx)
	if err != nil {
		return runner.Result{ExitCode: -1, Err: fmt.Errorf("acp: initialize: %w", err)}
	}
	slog.Info("acp runner: agent initialized",
		"job_id", req.JobID, "agent", agentName(initRes), "protocol_version", initRes.ProtocolVersion,
		"load_session", initRes.AgentCapabilities.LoadSession)

	sess, err := client.NewSession(ctx, acp.SessionNewParams{Cwd: req.WorkDir, MCPServers: mcpServers(req.ACP.MCPServers)})
	if err != nil {
		return runner.Result{ExitCode: -1, Err: fmt.Errorf("acp: session/new: %w", err)}
	}
	// The agent's mode block is S2's read_only lever; logging it here makes an S0
	// run's transcript answer "does this agent offer modes, and which ids?" without a
	// bespoke probe.
	slog.Info("acp runner: session opened",
		"job_id", req.JobID, "session_id", sess.SessionID,
		"mode", modeSummary(sess.Modes))

	res := runner.Result{SessionID: sess.SessionID}
	pr, perr := client.Prompt(ctx, sess.SessionID, req.ACP.Prompt, h)
	res.StopReason = pr.StopReason
	events.write(map[string]any{"t": "stop", "stop_reason": pr.StopReason})

	// A context-driven exit wins the classification: the job service maps the ctx
	// reason to cancelled/timeout, and the agent's cancelled stopReason is already
	// recorded above.
	if ctxErr := ctx.Err(); ctxErr != nil {
		res.ExitCode = -1
		res.Err = ctxErr
		return res
	}
	if perr != nil {
		res.ExitCode = -1
		res.Err = fmt.Errorf("acp: session/prompt: %w", perr)
		return res
	}

	switch pr.StopReason {
	case acp.StopEndTurn:
		res.ExitCode = 0
	case acp.StopMaxTokens, acp.StopMaxTurnRequests:
		// The turn ended early but the job did run to a defined end: done + warn.
		slog.Warn("acp runner: turn ended before the agent was finished",
			"job_id", req.JobID, "stop_reason", pr.StopReason)
		res.ExitCode = 0
	case acp.StopRefusal:
		res.ExitCode = 1
		res.Err = fmt.Errorf("acp: agent refused the prompt (stop_reason=%s)", pr.StopReason)
	case acp.StopCancelled:
		res.ExitCode = -1
		res.Err = errors.New("acp: agent cancelled the turn")
	default:
		res.ExitCode = 0
		slog.Warn("acp runner: unknown stopReason", "job_id", req.JobID, "stop_reason", pr.StopReason)
	}
	return res
}

// agentName returns the agent's self-reported name for logging ("" when absent).
func agentName(res acp.InitializeResult) string {
	if res.AgentInfo == nil {
		return ""
	}
	return res.AgentInfo.Name
}

// modeSummary renders a session's mode block for logging: "" when the agent
// reported none, else "<current> [<id>,<id>…]".
func modeSummary(m *acp.SessionModes) string {
	if m == nil {
		return ""
	}
	ids := make([]string, 0, len(m.AvailableModes))
	for _, mode := range m.AvailableModes {
		ids = append(ids, mode.ID)
	}
	return m.CurrentModeID + " [" + strings.Join(ids, ",") + "]"
}

// mcpServers converts the request's MCP servers into ACP's wire shape (env as a
// name/value list, ordered for a stable session/new payload).
func mcpServers(in []runner.ACPMCPServer) []acp.MCPServer {
	if len(in) == 0 {
		return nil
	}
	out := make([]acp.MCPServer, 0, len(in))
	for _, s := range in {
		srv := acp.MCPServer{Name: s.Name, Command: s.Command, Args: s.Args}
		if len(s.Env) > 0 {
			keys := make([]string, 0, len(s.Env))
			for k := range s.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				srv.Env = append(srv.Env, acp.EnvVariable{Name: k, Value: s.Env[k]})
			}
		}
		out = append(out, srv)
	}
	return out
}

// handler implements acp.Handler for one job: agent text to stdout, everything
// structured to the event stream, and tool-call status changes to job events.
type handler struct {
	stdout     io.Writer
	events     *eventWriter
	onJobEvent func(string, map[string]any)

	mu         sync.Mutex
	toolStatus map[string]string
}

// SessionUpdate implements acp.Handler.
func (h *handler) SessionUpdate(_ string, u acp.Update) {
	switch u.Kind {
	case acp.UpdateAgentMessageChunk:
		// The job's product: agent text merges straight into stdout.log.
		if u.MessageChunk != nil && u.MessageChunk.Text != "" {
			if _, err := io.WriteString(h.stdout, u.MessageChunk.Text); err != nil {
				slog.Debug("acp runner: write stdout", "err", err)
			}
		}
	case acp.UpdateAgentThoughtChunk:
		h.events.write(map[string]any{"t": "thought", "text": chunkText(u.MessageChunk)})
	case acp.UpdateToolCall, acp.UpdateToolCallUpdate:
		if u.ToolCall == nil {
			return
		}
		h.events.write(toolCallEvent(u.ToolCall))
		h.emitToolCallJobEvent(u.ToolCall)
	case acp.UpdatePlan:
		if u.Plan == nil {
			return
		}
		entries := make([]map[string]any, 0, len(u.Plan.Entries))
		for _, e := range u.Plan.Entries {
			entries = append(entries, map[string]any{"content": e.Content, "priority": e.Priority, "status": e.Status})
		}
		h.events.write(map[string]any{"t": "plan", "entries": entries})
	case acp.UpdateCurrentMode:
		h.events.write(map[string]any{"t": "mode", "mode_id": u.CurrentModeID})
	default:
		// A variant S0 does not model: keep the raw payload so the stream stays a
		// complete record of the turn.
		h.events.write(map[string]any{"t": u.Kind, "raw": truncate(string(u.Raw))})
	}
}

// emitToolCallJobEvent emits a job.tool_call event when a tool call's status
// CHANGES: the content-only refreshes an agent sends between statuses belong to the
// event stream, not to the job's lifecycle log.
func (h *handler) emitToolCallJobEvent(tc *acp.ToolCall) {
	if tc.Status == "" {
		return
	}
	h.mu.Lock()
	prev, seen := h.toolStatus[tc.ToolCallID]
	h.toolStatus[tc.ToolCallID] = tc.Status
	h.mu.Unlock()
	if seen && prev == tc.Status {
		return
	}
	if h.onJobEvent == nil {
		return
	}
	h.onJobEvent("job.tool_call", map[string]any{
		"tool_call_id": tc.ToolCallID,
		"title":        tc.Title,
		"kind":         tc.Kind,
		"status":       tc.Status,
	})
}

// RequestPermission implements acp.Handler with the S0 policy: auto-allow. The
// first allow_once option is chosen; failing that the first allow_always; if the
// agent offers neither, the request is cancelled (never run an unapproved tool
// call). S1 replaces this with the approval gate.
func (h *handler) RequestPermission(p acp.RequestPermissionParams) acp.PermissionOutcome {
	chosen := pickOption(p.Options, acp.OptionAllowOnce)
	if chosen == "" {
		chosen = pickOption(p.Options, acp.OptionAllowAlways)
	}
	options := make([]map[string]string, 0, len(p.Options))
	for _, o := range p.Options {
		options = append(options, map[string]string{"option_id": o.OptionID, "kind": o.Kind, "name": o.Name})
	}
	ev := map[string]any{"t": "permission_request", "options": options, "chosen": chosen}
	if p.ToolCall != nil {
		ev["tool_call_id"] = p.ToolCall.ToolCallID
		ev["kind"] = p.ToolCall.Kind
	}
	h.events.write(ev)
	if chosen == "" {
		return acp.PermissionCancelled()
	}
	return acp.PermissionSelected(chosen)
}

// pickOption returns the first offered optionId of the given kind ("" if none).
func pickOption(options []acp.PermissionOption, kind string) string {
	for _, o := range options {
		if o.Kind == kind {
			return o.OptionID
		}
	}
	return ""
}

// chunkText returns a content block's text (nil-safe).
func chunkText(c *acp.ContentBlock) string {
	if c == nil {
		return ""
	}
	return c.Text
}

// toolCallEvent renders one tool_call/tool_call_update line for acp.jsonl.
func toolCallEvent(tc *acp.ToolCall) map[string]any {
	ev := map[string]any{"t": "tool_call", "tool_call_id": tc.ToolCallID}
	if tc.Title != "" {
		ev["title"] = tc.Title
	}
	if tc.Kind != "" {
		ev["kind"] = tc.Kind
	}
	if tc.Status != "" {
		ev["status"] = tc.Status
	}
	if len(tc.Locations) > 0 {
		locs := make([]string, 0, len(tc.Locations))
		for _, l := range tc.Locations {
			locs = append(locs, l.Path)
		}
		ev["locations"] = locs
	}
	if len(tc.RawInput) > 0 {
		ev["raw_input"] = truncate(string(tc.RawInput))
	}
	if len(tc.RawOutput) > 0 {
		ev["raw_output"] = truncate(string(tc.RawOutput))
	}
	return ev
}

// truncate caps a raw payload embedded in an event line.
func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxRawBytes {
		return s
	}
	return s[:maxRawBytes] + "…(truncated)"
}

// eventWriter appends JSON lines to acp.jsonl. A nil writer drops events (the
// stream is best-effort); writes are serialised because notification handling and
// permission answers run on different goroutines.
type eventWriter struct {
	mu sync.Mutex
	f  *os.File
}

// openEventWriter creates <resultDir>/artifacts/acp.jsonl (truncating).
func openEventWriter(resultDir string) (*eventWriter, error) {
	if resultDir == "" {
		return &eventWriter{}, nil
	}
	dir := filepath.Join(resultDir, "artifacts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &eventWriter{}, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ACPFileName), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return &eventWriter{}, err
	}
	return &eventWriter{f: f}, nil
}

// write appends one event line. Oversized lines are replaced by a truncation
// marker rather than written invalid (or unbounded) JSON.
func (w *eventWriter) write(ev map[string]any) {
	if w == nil || w.f == nil {
		return
	}
	b, err := json.Marshal(ev)
	if err != nil {
		slog.Debug("acp runner: encode event", "err", err)
		return
	}
	if len(b) > maxEventLineBytes {
		kind, _ := ev["t"].(string)
		b, err = json.Marshal(map[string]any{"t": kind, "truncated": len(b)})
		if err != nil {
			return
		}
	}
	b = append(b, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.f.Write(b); err != nil {
		slog.Debug("acp runner: write event", "err", err)
	}
}

// Close closes the event stream (nil-safe, idempotent).
func (w *eventWriter) Close() error {
	if w == nil || w.f == nil {
		return nil
	}
	f := w.f
	w.f = nil
	return f.Close()
}
