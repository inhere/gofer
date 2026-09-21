// Package acp runs an `acp-agent` job: it starts the agent's ACP server over stdio,
// drives exactly one prompt turn (initialize → session/new → session/prompt) and maps
// the protocol's events onto the job's outputs.
//
// Where each thing goes (bd h-aii-rnxk / h-aii-7kja):
//
//   - stdout.log  — the agent's own text, one block per message (a tool call ends a
//     block, and the block after it starts on a fresh line).
//   - stderr.log  — the execution DETAILS as compact `{"type":…}` event lines in the
//     ndjson capture's shape, so the web's NdjsonTimeline and `job logs stderr` read
//     them: tool_call (per status change), thought (coalesced), permission, plan, stop.
//   - acp.jsonl   — the full structured stream for debugging (raw payloads truncated),
//     thoughts coalesced the same way.
//   - job events  — lifecycle only: the approval gate's permission_* rows and ONE
//     job.acp_summary at the end of the turn.
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
	"time"

	"github.com/inhere/gofer/internal/acp"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
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

// maxThoughtBytes caps the COALESCED thought text (bd h-aii-7kja ②). A thought is the
// agent talking to itself: the line is for orientation, not for reading the reasoning,
// and an unbounded accumulator would hand a runaway agent the job's memory.
const maxThoughtBytes = 2048

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
	// GATE-01: the approval policy is resolved by the job service (project policy
	// tightened by the agent's), but WithDefaults here is the runner's own guarantee
	// that it never has to reason about an unset field.
	policy := req.ACP.Approval.WithDefaults()

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

	h := &handler{
		ctx:         ctx,
		stdout:      req.Stdout,
		stderr:      req.Stderr,
		events:      events,
		onJobEvent:  req.OnJobEvent,
		policy:      policy,
		approvals:   req.Approvals,
		jobID:       req.JobID,
		logThoughts: req.ACP.LogThoughts,
		toolStatus:  map[string]string{},
	}
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

	// S2 (design §一.4): a continuation LOADS the source session instead of opening a
	// new one, so the agent starts the new turn with the previous turn's context. A
	// load the agent cannot serve is a HARD failure — falling back to session/new
	// would run the prompt in a context-free session while the job claims to continue
	// one. Both the capability check and a failed/served call record a stderr line so
	// the job explains itself without the runner's slog.
	load := req.ACP.LoadSessionID
	if load != "" && !initRes.AgentCapabilities.LoadSession {
		err := errors.New("acp: agent does not support session/load (agentCapabilities.loadSession=false)")
		writeStderrLine(req.Stderr, err.Error())
		return runner.Result{ExitCode: -1, Err: err}
	}
	var sess acp.SessionNewResult
	if load != "" {
		sess, err = client.LoadSession(ctx, acp.SessionLoadParams{
			SessionID:  load,
			Cwd:        req.WorkDir,
			MCPServers: mcpServers(req.ACP.MCPServers),
		})
		if err != nil {
			err = fmt.Errorf("acp: session/load %q: %w", load, err)
			writeStderrLine(req.Stderr, err.Error())
			return runner.Result{ExitCode: -1, Err: err}
		}
	} else {
		sess, err = client.NewSession(ctx, acp.SessionNewParams{Cwd: req.WorkDir, MCPServers: mcpServers(req.ACP.MCPServers)})
		if err != nil {
			return runner.Result{ExitCode: -1, Err: fmt.Errorf("acp: session/new: %w", err)}
		}
	}
	// The agent's mode block is S2's read_only lever; logging it here makes an S0
	// run's transcript answer "does this agent offer modes, and which ids?" without a
	// bespoke probe.
	slog.Info("acp runner: session opened",
		"job_id", req.JobID, "session_id", sess.SessionID,
		"mode", modeSummary(sess.Modes), "loaded", load != "")
	events.write(map[string]any{"t": "session", "load": load != "", "session_id": sess.SessionID, "mode": modeSummary(sess.Modes)})

	// bd h-aii-0ql3: a read-only job switches the session's agent mode before the turn.
	// The agent's mode list is authoritative: an id it does not offer must not be
	// guessed past, because the alternative is running a "read-only" job writable.
	if mode := req.ACP.ReadOnlyModeID; mode != "" {
		if !modeOffered(sess.Modes, mode) {
			err := fmt.Errorf("acp: read-only mode %q not offered by agent (available: %s)", mode, modeIDs(sess.Modes))
			writeStderrLine(req.Stderr, err.Error())
			return runner.Result{ExitCode: -1, Err: err}
		}
		if err := client.SetMode(ctx, sess.SessionID, mode); err != nil {
			err = fmt.Errorf("acp: session/set_mode %q: %w", mode, err)
			writeStderrLine(req.Stderr, err.Error())
			return runner.Result{ExitCode: -1, Err: err}
		}
		events.write(map[string]any{"t": "set_mode", "mode": mode})
		slog.Info("acp runner: session mode set", "job_id", req.JobID, "mode", mode)
	}

	res := runner.Result{SessionID: sess.SessionID}
	pr, perr := client.Prompt(ctx, sess.SessionID, req.ACP.Prompt, h)
	res.StopReason = pr.StopReason
	res.Usage = h.usageSnapshot()
	events.write(map[string]any{"t": "stop", "stop_reason": pr.StopReason})
	// The turn is over: close the stdout block, flush the last thought, and record the
	// turn's one lifecycle row (bd h-aii-rnxk). This happens for a cancelled/failed turn
	// too — what did run is still worth summarising.
	h.endTurn()
	h.emitSummary(pr.StopReason)

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

// writeStderrLine records a runner-side failure on the JOB's stderr log. A job that
// dies before the agent produces anything would otherwise leave stderr.log empty and
// the reason visible only in the server's slog — while stderr.log is what `job show`,
// the web console and the automatic-resume scan read.
func writeStderrLine(w io.Writer, line string) {
	if w == nil {
		return
	}
	_, _ = io.WriteString(w, line+"\n")
}

// modeOffered reports whether the session's advertised mode list permits modeID. An
// agent that reports NO modes (codex-acp today) is not second-guessed: the check is
// vacuous and set_mode is still attempted, because refusing to try would reject an
// agent whose support just is not advertised in the response.
func modeOffered(m *acp.SessionModes, modeID string) bool {
	if m == nil || len(m.AvailableModes) == 0 {
		return true
	}
	for _, mode := range m.AvailableModes {
		if mode.ID == modeID {
			return true
		}
	}
	return false
}

// modeIDs renders a session's available mode ids for an error message.
func modeIDs(m *acp.SessionModes) string {
	if m == nil || len(m.AvailableModes) == 0 {
		return "none reported"
	}
	ids := make([]string, 0, len(m.AvailableModes))
	for _, mode := range m.AvailableModes {
		ids = append(ids, mode.ID)
	}
	return strings.Join(ids, ", ")
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

// handler implements acp.Handler for one job: agent text to stdout, the execution
// details to stderr as compact event lines, everything structured to acp.jsonl, and the
// approval gate (GATE-01 §1) on permission requests. The job event timeline gets no
// per-tool-call rows — one job.acp_summary is all it records about the turn.
type handler struct {
	// ctx is the JOB's context: the approval wait is bounded by it, and a cancelled
	// job must unwind a pending approval instead of hanging the runner.
	ctx        context.Context
	stdout     io.Writer
	stderr     io.Writer
	events     *eventWriter
	onJobEvent func(string, map[string]any)
	// policy is the RESOLVED approval policy of this job.
	policy config.ApprovalConfig
	// approvals raises a permission interaction and blocks for the answer; nil when
	// the executing side has no interaction surface (then a gated call is cancelled,
	// never approved).
	approvals runner.ApprovalSink
	jobID     string
	// logThoughts keeps the coalesced thought line in the logs (acp.log_thoughts).
	logThoughts bool

	// mu guards the whole mutable state below AND the stdout/stderr writes: the ACP
	// client delivers session updates on its reader goroutine while the turn's end
	// (a trailing newline, the summary) is written by the runner's own goroutine.
	mu         sync.Mutex
	toolStatus map[string]string
	// toolCalls/thoughts/permissions are the turn's tallies for job.acp_summary.
	toolCalls   int
	thoughts    int
	permissions int
	// thought coalesces the agent's per-token thought stream into ONE line (bd
	// h-aii-7kja ②), flushed at the next boundary. Guarded by mu.
	thought strings.Builder
	// stdoutWrote reports whether the agent has written text; stdoutSep reports that a
	// detail (tool call / permission) ended the previous message block, so the next
	// text starts on a fresh block (bd h-aii-7kja ①). stdoutLast is the last byte
	// written, so a block can be closed with exactly one newline. Guarded by mu.
	stdoutWrote bool
	stdoutSep   bool
	stdoutLast  byte
	// usage is the token/cost tally the agent reported through usage_update events
	// (SUP-01 E), merged as they arrive; nil when it reported none. Guarded by mu.
	usage *runner.Usage
	// remembered marks the tool kinds a human answered allow_always for in THIS job
	// (remember_allow_always): the same kind stops re-asking. Guarded by mu.
	remembered map[string]bool
}

// SessionUpdate implements acp.Handler.
func (h *handler) SessionUpdate(_ string, u acp.Update) {
	switch u.Kind {
	case acp.UpdateAgentMessageChunk:
		// The job's product: agent text merges into stdout.log. A thought stream ends
		// here (the agent moved on to speaking) and the message block is separated from
		// the code before it.
		h.flushThought()
		h.writeStdout(chunkText(u.MessageChunk))
	case acp.UpdateAgentThoughtChunk:
		h.addThought(chunkText(u.MessageChunk))
	case acp.UpdateToolCall, acp.UpdateToolCallUpdate:
		if u.ToolCall == nil {
			return
		}
		h.flushThought()
		h.events.write(toolCallEvent(u.ToolCall))
		h.recordToolCall(u.ToolCall)
	case acp.UpdatePlan:
		if u.Plan == nil {
			return
		}
		entries := make([]map[string]any, 0, len(u.Plan.Entries))
		for _, e := range u.Plan.Entries {
			entries = append(entries, map[string]any{"content": e.Content, "priority": e.Priority, "status": e.Status})
		}
		h.events.write(map[string]any{"t": "plan", "entries": entries})
		h.writeStderr(compactLine("plan", func(e *ndjsonfilter.CompactEvent) { e.Add("entries", entries) }))
	case acp.UpdateCurrentMode:
		h.events.write(map[string]any{"t": "mode", "mode_id": u.CurrentModeID})
	case acp.UpdateUsage:
		// SUP-01 E: the agent's token/cost accounting. It stays in the event stream as
		// it always did, and the runner keeps the running tally so the job row can
		// record what the run cost.
		h.events.write(map[string]any{"t": u.Kind, "raw": truncate(string(u.Raw))})
		if got := usageFromUpdate(u.Raw); got != nil {
			h.mu.Lock()
			h.usage = overlayUsage(h.usage, got)
			h.mu.Unlock()
		}
	default:
		// A variant S0 does not model: keep the raw payload so the stream stays a
		// complete record of the turn.
		h.events.write(map[string]any{"t": u.Kind, "raw": truncate(string(u.Raw))})
	}
}

// writeStdout appends an agent message block to stdout.log, opening a new block when a
// detail (a tool call or a permission round trip) came since the last text — bd
// h-aii-7kja ①: "Hello world.Done." as one unbroken line is not readable.
func (h *handler) writeStdout(text string) {
	if text == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stdout == nil {
		return
	}
	if h.stdoutSep && h.stdoutWrote {
		h.stdoutWrite("\n")
	}
	h.stdoutSep = false
	h.stdoutWrite(text)
	h.stdoutWrote = true
	h.stdoutLast = text[len(text)-1]
}

// stdoutWrite writes to stdout.log under h.mu and records the last byte.
func (h *handler) stdoutWrite(s string) {
	if _, err := io.WriteString(h.stdout, s); err != nil {
		slog.Debug("acp runner: write stdout", "err", err)
	}
	h.stdoutLast = s[len(s)-1]
}

// detailBoundary marks the end of the current stdout message block: the tool call or
// permission that just happened is not part of the agent's prose, so the text after it
// starts a new block, and the block before it is closed with a newline.
func (h *handler) detailBoundary() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.detailBoundaryLocked()
}

func (h *handler) detailBoundaryLocked() {
	if !h.stdoutWrote {
		return
	}
	if h.stdoutLast != '\n' {
		h.stdoutWrite("\n")
	}
	h.stdoutSep = true
}

// endTurn closes the turn's stdout block and flushes whatever is left in the thought
// buffer. Called once the prompt turn is over (also on a failed/cancelled turn: the
// text that did arrive must end its own block).
func (h *handler) endTurn() {
	h.flushThought()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stdoutWrote && h.stdoutLast != '\n' && h.stdout != nil {
		h.stdoutWrite("\n")
	}
}

// addThought accumulates one thought shard (bd h-aii-7kja ②). Nothing is written until
// the next boundary, which is what turns a per-token stream into one line.
func (h *handler) addThought(text string) {
	if text == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.thoughts++
	if !h.logThoughts || h.thought.Len() >= maxThoughtBytes {
		return
	}
	if room := maxThoughtBytes - h.thought.Len(); len(text) > room {
		text = text[:room]
	}
	h.thought.WriteString(text)
}

// flushThought writes the coalesced thought — one acp.jsonl line and one compact stderr
// event — and resets the buffer.
func (h *handler) flushThought() {
	h.mu.Lock()
	text := h.thought.String()
	h.thought.Reset()
	h.mu.Unlock()
	if text == "" {
		return
	}
	h.events.write(map[string]any{"t": "thought", "text": text})
	h.writeStderr(compactLine("thought", func(e *ndjsonfilter.CompactEvent) { e.Add("text", text) }))
}

// recordToolCall projects a tool-call update onto the job's own surfaces: the compact
// stderr line (per STATUS CHANGE — the content-only refreshes between statuses belong to
// acp.jsonl alone) and the turn's tally. The job timeline gets nothing: its one row
// about the turn is job.acp_summary.
func (h *handler) recordToolCall(tc *acp.ToolCall) {
	if tc.Status == "" {
		return
	}
	h.mu.Lock()
	prev, seen := h.toolStatus[tc.ToolCallID]
	h.toolStatus[tc.ToolCallID] = tc.Status
	if !seen {
		h.toolCalls++
	}
	h.mu.Unlock()

	h.detailBoundary()
	if seen && prev == tc.Status {
		return
	}
	h.writeStderr(compactLine("tool_call", func(e *ndjsonfilter.CompactEvent) {
		e.Add("id", tc.ToolCallID).Add("title", tc.Title).Add("kind", tc.Kind).Add("status", tc.Status)
	}))
}

// writeStderr appends one compact event line (newline-terminated) to the job's stderr.
// A nil writer — a runner-only unit test — drops it.
func (h *handler) writeStderr(line []byte) {
	if h.stderr == nil || len(line) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.stderr.Write(append(line, '\n')); err != nil {
		slog.Debug("acp runner: write stderr", "err", err)
	}
}

// compactLine renders one compact event line in the ndjson capture's shape
// ({"type":…}), which is what NdjsonTimeline and `job logs stderr` render, capped at
// ndjsonfilter.DefaultMaxEventBytes.
func compactLine(typ string, fill func(*ndjsonfilter.CompactEvent)) []byte {
	e := ndjsonfilter.NewCompactEvent(typ)
	fill(e)
	return e.Line()
}

// emitSummary records the turn's ONE lifecycle row on the job (bd h-aii-rnxk) plus the
// matching compact `stop` event on stderr.
func (h *handler) emitSummary(stopReason string) {
	h.writeStderr(compactLine("stop", func(e *ndjsonfilter.CompactEvent) { e.Add("reason", stopReason) }))
	if h.onJobEvent == nil {
		return
	}
	h.mu.Lock()
	detail := map[string]any{
		"tool_calls":  h.toolCalls,
		"thoughts":    h.thoughts,
		"permissions": h.permissions,
	}
	h.mu.Unlock()
	if stopReason != "" {
		detail["stop_reason"] = stopReason
	}
	h.onJobEvent(runner.EventACPSummary, detail)
}

// usageSnapshot returns the tally the agent reported through usage_update events
// (SUP-01 E), or nil when it reported none. Called once the turn is over, so the
// value it returns is the run's final accounting.
func (h *handler) usageSnapshot() *runner.Usage {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.usage
}

// RequestPermission implements acp.Handler with the job's approval policy (GATE-01
// §1). Mode off auto-allows (the S0 behaviour); ask auto-allows the policy's
// auto_allow_kinds and remembers an allow_always answer for the kind; everything else
// is put to a human as a `permission` interaction and the agent waits for the answer.
func (h *handler) RequestPermission(p acp.RequestPermissionParams) acp.PermissionOutcome {
	kind := ""
	if p.ToolCall != nil {
		kind = p.ToolCall.Kind
	}
	switch {
	case h.policy.Mode == config.ApprovalOff:
		return h.answerAutomatically(p, kind, "off")
	case h.rememberedAllowAlways(kind):
		return h.answerAutomatically(p, kind, "remembered_allow_always")
	case h.policy.Mode == config.ApprovalAsk && h.policy.AutoAllow(kind):
		return h.answerAutomatically(p, kind, "auto_allow_kind")
	default:
		return h.askApprover(p, kind)
	}
}

// answerAutomatically resolves a request without a human: allow_once, falling back to
// allow_always — except in the remembered case, where the human's allow_always answer
// is what the agent is told again. An option the agent never offered is never invented;
// with neither offered the request is cancelled (never run an unapproved tool call).
func (h *handler) answerAutomatically(p acp.RequestPermissionParams, kind, reason string) acp.PermissionOutcome {
	preferred, fallback := acp.OptionAllowOnce, acp.OptionAllowAlways
	if reason == "remembered_allow_always" {
		preferred, fallback = acp.OptionAllowAlways, acp.OptionAllowOnce
	}
	chosen := pickOption(p.Options, preferred)
	if chosen == "" {
		chosen = pickOption(p.Options, fallback)
	}
	if chosen == "" {
		h.recordPermission(p, kind, "cancelled", "", "", true, reason)
		return acp.PermissionCancelled()
	}
	optionKind := kindOfOption(p.Options, chosen)
	h.recordPermission(p, kind, "selected", chosen, optionKind, true, reason)
	if h.onJobEvent != nil {
		h.onJobEvent(runner.EventPermissionAnswered, map[string]any{
			"option_id": chosen, "kind": optionKind, "by": "", "auto": true,
		})
	}
	return acp.PermissionSelected(chosen)
}

// askApprover is the gate proper: it raises a permission interaction carrying the tool
// call and the agent's options, and blocks until the answer arrives, the approval
// deadline passes, or the job ends. The runner (not the caller) owns the timeout
// policy, so an unanswered request is resolved by on_timeout rather than by a deadlock.
func (h *handler) askApprover(p acp.RequestPermissionParams, kind string) acp.PermissionOutcome {
	prompt := "Approve tool call"
	if title := toolCallTitle(p.ToolCall); title != "" {
		prompt = fmt.Sprintf("Approve tool call %q", title)
	}
	if kind != "" {
		prompt = fmt.Sprintf("%s (kind=%s)", prompt, kind)
	}
	hint := h.policy.Mode + ": kind=" + kind

	if h.approvals == nil {
		// No interaction surface to ask through: never run an unapproved tool call.
		slog.Warn("acp runner: approval gate has no interaction surface; cancelling the request",
			"job_id", h.jobID, "kind", kind)
		h.recordPermission(p, kind, "cancelled", "", "", false, "no_approval_surface")
		return acp.PermissionCancelled()
	}

	ctx, cancel := context.WithTimeout(h.ctx, time.Duration(h.policy.TimeoutSec)*time.Second)
	defer cancel()
	var interactionID string
	out, err := h.approvals.RequestApproval(ctx, runner.ApprovalRequest{
		Prompt:     prompt,
		Options:    approvalRequestOptions(p.Options),
		ToolCall:   approvalRequestToolCall(p.ToolCall),
		PolicyHint: hint,
		TimeoutSec: h.policy.TimeoutSec,
		// The gate now waits on a human: announce it as soon as the card exists (this
		// is the event a webhook subscribes to for an approval notification), not
		// after the answer.
		OnRaised: func(id string) {
			interactionID = id
			h.writeStderr(compactLine("permission", func(e *ndjsonfilter.CompactEvent) {
				e.Add("state", "requested").
					Add("id", toolCallID(p.ToolCall)).
					Add("kind", kind).
					Add("title", toolCallTitle(p.ToolCall))
			}))
			if h.onJobEvent == nil {
				return
			}
			h.onJobEvent(runner.EventPermissionRequested, map[string]any{
				"interaction_id": id,
				"tool_call_id":   toolCallID(p.ToolCall),
				"kind":           kind,
				"title":          toolCallTitle(p.ToolCall),
				"policy_hint":    hint,
			})
		},
	})
	if err == nil {
		if out.Answer == "" {
			// Cancelled instead of answered (the job ended underneath us).
			h.recordPermission(p, kind, "cancelled", "", "", false, "cancelled")
			return acp.PermissionCancelled()
		}
		optionKind := kindOfOption(p.Options, out.Answer)
		if optionKind == "" {
			// An answer that matches no offered option cannot be relayed as `selected`
			// (the protocol demands one of the agent's optionIds): cancel rather than
			// lie to the agent about what was approved.
			slog.Warn("acp runner: approval answer is not one of the offered options",
				"job_id", h.jobID, "answer", out.Answer)
			h.recordPermission(p, kind, "cancelled", out.Answer, "", false, "unknown_option")
			return acp.PermissionCancelled()
		}
		if optionKind == acp.OptionAllowAlways && h.policy.AllowsAlways() {
			h.rememberAllowAlways(kind)
		}
		h.recordPermission(p, kind, "selected", out.Answer, optionKind, false, out.By)
		if h.onJobEvent != nil {
			h.onJobEvent(runner.EventPermissionAnswered, map[string]any{
				"option_id": out.Answer, "kind": optionKind, "by": out.By, "auto": false,
			})
		}
		return acp.PermissionSelected(out.Answer)
	}
	if h.ctx.Err() != nil {
		// The JOB ended (cancel/timeout): the turn is being torn down, so the agent
		// gets a cancellation and the job's own status is decided by the runner's
		// context handling — no timeout policy applies to a dead job.
		h.recordPermission(p, kind, "cancelled", "", "", false, "job_ended")
		return acp.PermissionCancelled()
	}
	// The approval deadline passed (the sink returns the ctx error it gave up on).
	return h.answerOnTimeout(p, kind, hint, interactionID, err)
}

// answerOnTimeout applies the project's on_timeout policy to a request nobody
// answered: allow answers allow_once; anything else answers reject_once, and when the
// agent offered no reject_once the request is CANCELLED rather than answered with an
// option it never offered.
func (h *handler) answerOnTimeout(p acp.RequestPermissionParams, kind, hint, interactionID string, waitErr error) acp.PermissionOutcome {
	chosen, optionKind := "", ""
	if h.policy.OnTimeout == config.ApprovalOnTimeoutAllow {
		chosen = pickOption(p.Options, acp.OptionAllowOnce)
		optionKind = acp.OptionAllowOnce
	} else {
		chosen = pickOption(p.Options, acp.OptionRejectOnce)
		optionKind = acp.OptionRejectOnce
	}
	slog.Warn("acp runner: approval timed out",
		"job_id", h.jobID, "kind", kind, "on_timeout", h.policy.OnTimeout,
		"timeout_sec", h.policy.TimeoutSec, "err", waitErr)
	detail := map[string]any{
		"tool_call_id": toolCallID(p.ToolCall),
		"kind":         kind,
		"title":        toolCallTitle(p.ToolCall),
		"on_timeout":   h.policy.OnTimeout,
		"policy_hint":  hint,
	}
	if interactionID != "" {
		detail["interaction_id"] = interactionID
	}
	if chosen == "" {
		h.recordPermission(p, kind, "cancelled", "", "", false, "timeout_no_option")
		if h.onJobEvent != nil {
			h.onJobEvent(runner.EventPermissionTimedOut, detail)
		}
		return acp.PermissionCancelled()
	}
	h.recordPermission(p, kind, "selected", chosen, optionKind, true, "timeout")
	if h.onJobEvent != nil {
		h.onJobEvent(runner.EventPermissionAnswered, map[string]any{
			"option_id": chosen, "kind": optionKind, "by": "", "auto": true,
		})
		h.onJobEvent(runner.EventPermissionTimedOut, detail)
	}
	return acp.PermissionSelected(chosen)
}

// recordPermission appends the approval decision to acp.jsonl (the job's audit trail:
// every permission request the agent made and what gofer answered, with why) and writes
// the compact stderr event for it; it also counts toward the turn's job.acp_summary.
func (h *handler) recordPermission(p acp.RequestPermissionParams, kind, outcome, optionID, optionKind string, auto bool, why string) {
	options := make([]map[string]string, 0, len(p.Options))
	for _, o := range p.Options {
		options = append(options, map[string]string{"option_id": o.OptionID, "kind": o.Kind, "name": o.Name})
	}
	ev := map[string]any{
		"t":            "permission",
		"tool_call_id": toolCallID(p.ToolCall),
		"kind":         kind,
		"title":        toolCallTitle(p.ToolCall),
		"mode":         h.policy.Mode,
		"options":      options,
		"outcome":      outcome,
		"auto":         auto,
		"reason":       why,
	}
	if optionID != "" {
		ev["option_id"] = optionID
	}
	if optionKind != "" {
		ev["option_kind"] = optionKind
	}
	if p.ToolCall != nil && len(p.ToolCall.RawInput) > 0 {
		ev["raw_input"] = truncate(string(p.ToolCall.RawInput))
	}
	h.events.write(ev)

	h.mu.Lock()
	h.permissions++
	h.mu.Unlock()
	h.writeStderr(compactLine("permission", func(e *ndjsonfilter.CompactEvent) {
		e.Add("state", outcome).
			Add("id", toolCallID(p.ToolCall)).
			Add("kind", kind).
			Add("title", toolCallTitle(p.ToolCall)).
			Add("option_id", optionID).
			Add("auto", auto).
			Add("reason", why)
	}))
}

// rememberedAllowAlways reports whether a human already chose allow_always for this
// tool kind in THIS job (remember_allow_always), so the same kind stops re-asking.
func (h *handler) rememberedAllowAlways(kind string) bool {
	if kind == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.remembered[kind]
}

// rememberAllowAlways records an allow_always answer for a tool kind (per job only —
// the gate is deliberately not a cross-job grant).
func (h *handler) rememberAllowAlways(kind string) {
	if kind == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.remembered == nil {
		h.remembered = map[string]bool{}
	}
	h.remembered[kind] = true
}

// kindOfOption returns the ACP kind of the option with this optionId ("" when the id
// matches nothing — an answer that selects an option the agent never offered).
func kindOfOption(options []acp.PermissionOption, optionID string) string {
	if optionID == "" {
		return ""
	}
	for _, o := range options {
		if o.OptionID == optionID {
			return o.Kind
		}
	}
	return ""
}

// approvalRequestOptions projects the agent's options onto the runner-neutral shape.
func approvalRequestOptions(options []acp.PermissionOption) []runner.ApprovalOption {
	if len(options) == 0 {
		return nil
	}
	out := make([]runner.ApprovalOption, 0, len(options))
	for _, o := range options {
		out = append(out, runner.ApprovalOption{ID: o.OptionID, Label: o.Name, Kind: o.Kind})
	}
	return out
}

// approvalRequestToolCall projects the gated tool call onto the runner-neutral shape,
// summarising rawInput (an approver needs the shape, not a megabyte payload).
func approvalRequestToolCall(tc *acp.ToolCall) *runner.ApprovalToolCall {
	if tc == nil {
		return nil
	}
	out := &runner.ApprovalToolCall{
		ID:              tc.ToolCallID,
		Title:           tc.Title,
		Kind:            tc.Kind,
		RawInputSummary: truncate(string(tc.RawInput)),
	}
	for _, l := range tc.Locations {
		out.Locations = append(out.Locations, l.Path)
	}
	return out
}

// toolCallID / toolCallTitle read a possibly-absent tool call.
func toolCallID(tc *acp.ToolCall) string {
	if tc == nil {
		return ""
	}
	return tc.ToolCallID
}

func toolCallTitle(tc *acp.ToolCall) string {
	if tc == nil {
		return ""
	}
	return tc.Title
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
