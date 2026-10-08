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
//     them: tool_call (per status or content/location change), thought (coalesced),
//     permission, plan, stop.
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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/acp"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
	"github.com/inhere/gofer/internal/util"
)

// Name is the runner identifier ("acp"). It is NOT a configurable runner key: the
// job service selects it for acp-agent jobs the way it selects the pty runner for
// interactive ones.
const Name = "acp"

// ACPFileName is the structured event stream this runner writes, relative to the
// job result dir. It sits under artifacts/ so it is part of the job's artifact
// manifest (listable + downloadable like any other artifact).
const ACPFileName = "acp.jsonl"

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

	events, err := openEventWriter(req.ACP.ResultDir, req.ACP.AppendEvents)
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
		toolEvents:  map[string]string{},
	}
	// F14: when the agent's config asks for it, the process starts with the `env` block
	// of claude's user settings file layered in — keys neither the process environment
	// nor the job's own env defines, minus SEC-01's denied ones. MergeEnv returns
	// req.Env itself when nothing is added, so a run without the switch (or without a
	// settings file) keeps the identical environment it had before.
	env := req.Env
	if req.ACP.ClaudeSettingsEnv {
		env = util.MergeEnv(req.Env, claudeSettingsExtra(req.JobID, req.Env, req.EnvDeny, req.EnvAllow))
	}
	client, err := acp.Start(ctx, acp.Options{
		Command:  req.Command,
		Args:     req.Args,
		Dir:      req.WorkDir,
		Env:      env,
		EnvDeny:  req.EnvDeny,
		EnvAllow: req.EnvAllow,
		Stderr:   req.Stderr,
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
	// N1 §B: pick the requested model BEFORE the first prompt. A model the agent cannot
	// take is a hard failure — answering with some other model while the job claims
	// this one would be a silent lie.
	if model := req.ACP.ModelID; model != "" {
		how, err := selectModel(ctx, client, sess, model)
		if err != nil {
			writeStderrLine(req.Stderr, err.Error())
			return runner.Result{ExitCode: -1, Err: err}
		}
		events.write(map[string]any{"t": "set_model", "model": model, "via": how})
		slog.Info("acp runner: session model set", "job_id", req.JobID, "model", model, "via", how)
	}
	if req.ACP.SessionCommands != nil {
		if req.ACP.OnSessionReady != nil {
			req.ACP.OnSessionReady(sess.SessionID)
		}
		return runResident(ctx, req, client, h, events, sess.SessionID)
	}

	events.write(promptEvent(req.ACP.Prompt))
	res := runner.Result{SessionID: sess.SessionID}
	pr, perr := client.Prompt(ctx, sess.SessionID, req.ACP.Prompt, h)
	res.StopReason = pr.StopReason
	res.Usage = h.usageSnapshot()
	// The turn is over: close the stdout block, flush the last thought, and record the
	// turn's one lifecycle row (bd h-aii-rnxk). This happens for a cancelled/failed turn
	// too — what did run is still worth summarising.
	h.endTurn()
	events.write(map[string]any{"t": "stop", "stop_reason": pr.StopReason})
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

// runResident drives all turns on the same initialized ACP process. Job owns the
// command channel and status callbacks; the runner only owns protocol and output.
func runResident(ctx context.Context, req runner.Request, client *acp.Client, h *handler, events *eventWriter, sessionID string) runner.Result {
	result := runner.Result{SessionID: sessionID}
	sessionCtx := ctx
	if req.ACP.MaxSessionSec > 0 {
		var cancel context.CancelFunc
		remaining := time.Duration(req.ACP.MaxSessionSec) * time.Second
		if req.ACP.MaxSessionDeadlineAt > 0 {
			remaining = time.Until(time.Unix(req.ACP.MaxSessionDeadlineAt, 0))
		}
		sessionCtx, cancel = context.WithTimeout(ctx, remaining)
		defer cancel()
	}
	prompt := req.ACP.Prompt
	turn := req.ACP.InitialTurnNo
	for {
		if prompt != "" {
			if req.ACP.OnTurnStart != nil {
				if err := req.ACP.OnTurnStart(); err != nil {
					return runner.Result{SessionID: sessionID, ExitCode: -1, Err: err}
				}
			}
			turn++
			h.resetTurn()
			if req.Stdout != nil {
				_, _ = fmt.Fprintf(req.Stdout, "--- turn %d ---\n", turn)
			}
			events.write(map[string]any{"t": "turn_started", "turn": turn})
			events.write(promptEvent(prompt))
			turnCtx := sessionCtx
			cancelTurn := func() {}
			if req.ACP.TurnTimeoutSec > 0 {
				turnCtx, cancelTurn = context.WithTimeout(sessionCtx, time.Duration(req.ACP.TurnTimeoutSec)*time.Second)
			}
			response, err := client.Prompt(turnCtx, sessionID, prompt, h)
			turnErr := turnCtx.Err()
			cancelTurn()
			h.endTurn()
			h.emitSummary(response.StopReason)
			events.write(map[string]any{"t": "turn_ended", "turn": turn, "stop_reason": response.StopReason})
			result.StopReason = response.StopReason
			result.Usage = h.usageSnapshot()
			if ctx.Err() != nil {
				result.ExitCode, result.Err = -1, ctx.Err()
				return result
			}
			if sessionCtx.Err() != nil {
				result.ExitCode, result.SessionEndReason = 0, "max_session_timeout"
				return result
			}
			if turnErr == context.DeadlineExceeded {
				result.ExitCode, result.Err = -1, fmt.Errorf("acp: turn timed out after %ds", req.ACP.TurnTimeoutSec)
				return result
			}
			if err != nil {
				result.ExitCode, result.Err = -1, fmt.Errorf("acp: session/prompt: %w", err)
				return result
			}
			if response.StopReason == acp.StopRefusal || response.StopReason == acp.StopCancelled {
				result.ExitCode, result.Err = 1, fmt.Errorf("acp: agent ended turn (stop_reason=%s)", response.StopReason)
				return result
			}
			if req.ACP.OnTurnEnd != nil {
				if err := req.ACP.OnTurnEnd(response.StopReason); err != nil {
					result.ExitCode, result.Err = -1, err
					return result
				}
			}
		}
		if req.ACP.OnAwaitInput != nil {
			if err := req.ACP.OnAwaitInput(); err != nil {
				result.ExitCode, result.Err = -1, err
				return result
			}
		}
		events.write(map[string]any{"t": "awaiting_input", "turn": turn})
		var idleTimer *time.Timer
		var idleC <-chan time.Time
		if req.ACP.IdleTimeoutSec > 0 {
			remaining := time.Duration(req.ACP.IdleTimeoutSec) * time.Second
			if req.ACP.LoadSessionID != "" && turn == req.ACP.InitialTurnNo && req.ACP.IdleDeadlineAt > 0 {
				remaining = time.Until(time.Unix(req.ACP.IdleDeadlineAt, 0))
			}
			idleTimer = time.NewTimer(remaining)
			idleC = idleTimer.C
		}
		select {
		case command, ok := <-req.ACP.SessionCommands:
			if idleTimer != nil {
				idleTimer.Stop()
			}
			if !ok {
				result.ExitCode, result.Err = -1, errors.New("acp: session command channel closed")
				return result
			}
			if command.End {
				if err := client.Cancel(sessionID); err != nil {
					slog.Debug("acp runner: session/cancel before end", "job_id", req.JobID, "err", err)
				}
				events.write(map[string]any{"t": "stop", "stop_reason": "manual_end"})
				result.ExitCode, result.SessionEndReason = 0, "manual_end"
				return result
			}
			prompt = command.Prompt
		case <-idleC:
			result.ExitCode, result.SessionEndReason = 0, "idle_timeout"
			return result
		case <-sessionCtx.Done():
			if idleTimer != nil {
				idleTimer.Stop()
			}
			if ctx.Err() == nil {
				result.ExitCode, result.SessionEndReason = 0, "max_session_timeout"
				return result
			}
			result.ExitCode, result.Err = -1, ctx.Err()
			return result
		case <-client.Done():
			if idleTimer != nil {
				idleTimer.Stop()
			}
			result.ExitCode, result.Err = -1, client.DeadErr()
			return result
		case <-ctx.Done():
			if idleTimer != nil {
				idleTimer.Stop()
			}
			result.ExitCode, result.Err = -1, ctx.Err()
			return result
		}
	}
}

func (h *handler) resetTurn() {
	h.mu.Lock()
	h.toolStatus = map[string]string{}
	h.toolEvents = map[string]string{}
	h.toolCalls, h.thoughts, h.permissions, h.permissionsAuto = 0, 0, 0, 0
	h.stdoutWrote, h.stdoutSep, h.stdoutLast = false, false, 0
	h.mu.Unlock()
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
	toolEvents map[string]string
	// toolCalls/thoughts/permissions/permissionsAuto are the turn's tallies for
	// job.acp_summary. permissions counts every request the agent made; permissionsAuto
	// the ones gofer answered without a human (off / auto_allow_kind / remembered /
	// timeout) — those have no timeline row of their own (F4).
	toolCalls       int
	thoughts        int
	permissions     int
	permissionsAuto int
	// thought coalesces the agent's per-token thought stream into ONE line (bd
	// h-aii-7kja ②), flushed at the next boundary. Guarded by mu.
	thought strings.Builder
	// message coalesces agent-message chunks until a structured boundary, a 2s idle
	// interval, or the per-record size guard. messageGeneration invalidates timer
	// callbacks that raced with a boundary flush. Guarded by mu.
	message           strings.Builder
	messageTimer      *time.Timer
	messageGeneration uint64
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
		text := chunkText(u.MessageChunk)
		h.writeStdout(text)
		h.addMessage(text)
	case acp.UpdateAgentThoughtChunk:
		h.flushMessage()
		h.addThought(chunkText(u.MessageChunk))
	case acp.UpdateToolCall, acp.UpdateToolCallUpdate:
		if u.ToolCall == nil {
			return
		}
		h.flushMessage()
		h.flushThought()
		h.events.write(toolCallEvent(u.ToolCall))
		h.recordToolCall(u.ToolCall)
	case acp.UpdatePlan:
		if u.Plan == nil {
			return
		}
		h.flushMessage()
		h.flushThought()
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
	h.flushMessage()
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
		// Cut to the byte budget on a RUNE boundary: an agent thinking in Chinese would
		// otherwise leave half a character at the cap (JSON-encodable, but garbage).
		text = text[:room]
		for len(text) > 0 && !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
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

// recordToolCall projects each distinct tool-call status/content/location snapshot onto
// the compact stderr line and the turn's tally. Identical refreshes are coalesced. The
// job timeline gets nothing: its one row about the turn is job.acp_summary.
func (h *handler) recordToolCall(tc *acp.ToolCall) {
	if tc == nil {
		return
	}
	event := toolCallEvent(tc)
	payload, _ := json.Marshal(event)
	h.mu.Lock()
	_, seenStatus := h.toolStatus[tc.ToolCallID]
	_, seenEvent := h.toolEvents[tc.ToolCallID]
	seen := seenStatus || seenEvent
	if !seen {
		h.toolCalls++
	}
	if tc.Status != "" {
		h.toolStatus[tc.ToolCallID] = tc.Status
	}
	if h.toolEvents == nil {
		h.toolEvents = make(map[string]string)
	}
	digest := sha256.Sum256(payload)
	fingerprint := string(digest[:])
	changed := !seen || h.toolEvents[tc.ToolCallID] != fingerprint
	h.toolEvents[tc.ToolCallID] = fingerprint
	h.mu.Unlock()

	h.detailBoundary()
	if !changed {
		return
	}
	h.writeStderr(compactLine("tool_call", func(e *ndjsonfilter.CompactEvent) {
		e.Add("id", tc.ToolCallID).Add("title", tc.Title).Add("kind", tc.Kind).Add("status", tc.Status)
		if content, truncated := boundedToolContent(tc.Content); len(content) > 0 {
			e.Add("content", string(content))
			if truncated {
				e.Add("content_truncated", "true")
			}
		} else if truncated {
			e.Add("content_truncated", "true")
		}
		if len(tc.Locations) > 0 {
			if locations, err := json.Marshal(tc.Locations); err == nil {
				e.Add("locations", string(locations))
			}
		}
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
		"tool_calls":       h.toolCalls,
		"thoughts":         h.thoughts,
		"permissions":      h.permissions,
		"permissions_auto": h.permissionsAuto,
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
//
// An automatic answer is NOT a timeline event (F4): one row per tool call made an
// `approval: off` job unreadable, while the same decision is already in acp.jsonl (the
// audit trail), on stderr as a compact event, and counted by job.acp_summary.
// job.permission_answered therefore means "a HUMAN answered" — do not emit it here.
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
	// The chosen option rides on the timeout row: the answer was automatic (see
	// answerAutomatically — no job.permission_answered here), so this is the only
	// timeline row that says what the agent was actually told.
	detail["option_id"] = chosen
	detail["option_kind"] = optionKind
	if h.onJobEvent != nil {
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
	if auto {
		h.permissionsAuto++
	}
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

// selectModel picks model on the session over the protocol and returns how it did.
// Order: a session config option of category "model" (session/set_config_option, the
// current protocol), else the agent's `models` block (session/set_model). The agent's
// own value list is authoritative when it reports one: an id it does not offer is an
// error naming what it does offer. An agent exposing neither cannot take --model.
func selectModel(ctx context.Context, client *acp.Client, sess acp.SessionNewResult, model string) (string, error) {
	for _, opt := range sess.ConfigOptions {
		if opt.Category != "model" && !(opt.Category == "" && opt.ID == "model") {
			continue
		}
		if len(opt.Options) > 0 {
			offered := make([]string, 0, len(opt.Options))
			found := false
			for _, v := range opt.Options {
				offered = append(offered, v.Value)
				if v.Value == model {
					found = true
				}
			}
			if !found {
				return "", fmt.Errorf("acp: model %q not offered by agent (available: %s)", model, strings.Join(offered, ", "))
			}
		}
		if err := client.SetConfigOption(ctx, sess.SessionID, opt.ID, model); err != nil {
			return "", fmt.Errorf("acp: session/set_config_option %s=%q: %w", opt.ID, model, err)
		}
		return "config_option", nil
	}
	if m := sess.Models; m != nil {
		if len(m.AvailableModels) > 0 {
			offered := make([]string, 0, len(m.AvailableModels))
			found := false
			for _, v := range m.AvailableModels {
				offered = append(offered, v.ModelID)
				if v.ModelID == model {
					found = true
				}
			}
			if !found {
				return "", fmt.Errorf("acp: model %q not offered by agent (available: %s)", model, strings.Join(offered, ", "))
			}
		}
		if err := client.SetModel(ctx, sess.SessionID, model); err != nil {
			return "", fmt.Errorf("acp: session/set_model %q: %w", model, err)
		}
		return "set_model", nil
	}
	return "", fmt.Errorf("acp: agent does not expose model selection (no session config option of category \"model\" and no models block), so --model %q cannot be applied", model)
}
