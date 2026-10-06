package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/procattr"
	"github.com/inhere/gofer/internal/proctree"
	"github.com/inhere/gofer/internal/util"
)

// cancelGrace bounds how long Prompt waits for the agent's stopReason cancelled
// response after session/cancel was sent. When it expires the client returns the
// context error anyway; the caller then kills the process (Close). It is deliberately
// SHORT (F12, 2026-09-25): the grace exists only to salvage the agent's stopReason, and
// the agent replies to session/cancel within a local protocol round trip — an
// unresponsive agent must not hold a cancelled job in `running` while the sums add up
// (the old 10s, on top of an unbounded cmd.Wait, is exactly how a cancelled and
// long-timed-out ACP job stayed running).
const cancelGrace = time.Second

// closeGrace bounds how long Close waits for the agent after terminating its
// process tree. The later fallback covers a failed or delayed tree kill.
const closeGrace = 750 * time.Millisecond

// waitDelay bounds how long cmd.Wait blocks on the stdout/stderr copy after the process
// itself has exited. A descendant that inherited the agent's stdio (npx → node → the
// adapter) keeps a copy goroutine waiting for an EOF that cannot come; without this the
// job would never reach a terminal state (the ACP-02 hang). Mirrors the local runner's
// stdioWaitDelay.
const waitDelay = time.Second

// waitAfterKill bounds how long Close waits for cmd.Wait after killing the tree. With
// waitDelay in force Wait returns shortly by itself; this is the final guarantee that
// Close — and therefore the job's terminal state — cannot be held hostage by a process
// that refuses to be reaped.
const waitAfterKill = 2 * time.Second

// Options configures Start.
type Options struct {
	// Command is the ACP agent executable; Args its argv (the launch argv, never a
	// prompt — the prompt travels over the protocol).
	Command string
	Args    []string
	// Dir is the working directory (the job's resolved cwd).
	Dir string
	// Env is layered over the process environment, like the local runner does.
	Env map[string]string
	// EnvDeny / EnvAllow filter the INHERITED process environment before Env is
	// layered on (SEC-01, util.EnvironWithout): an ACP agent is a job process too, so
	// it must not start holding the serve process's bearer token. See
	// runner.Request.EnvDeny.
	EnvDeny  []string
	EnvAllow []string
	// Stderr receives the agent's own diagnostics verbatim (the protocol channel is
	// stdin/stdout only). Nil discards them.
	Stderr io.Writer
	// ClientInfo identifies this client in initialize. Nil means {Name: "gofer"}.
	ClientInfo *Implementation
}

// DefaultClientVersion is the clientInfo.version advertised when Options.ClientInfo
// carries none. A version is REQUIRED by the ACP schema in practice — the
// claude-code-acp adapter rejects an initialize whose clientInfo has no version
// (-32602 Invalid params) — so the client never omits it.
const DefaultClientVersion = "0.0.0"

// defaultClientVersion reports this binary's version as seen by the Go runtime
// (linker-injected for a release build), falling back to DefaultClientVersion for
// a development build. It spares every caller from threading build metadata down
// to the ACP client just to fill initialize's clientInfo.
func defaultClientVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return DefaultClientVersion
	}
	v := strings.TrimSpace(bi.Main.Version)
	if v == "" || v == "(devel)" {
		return DefaultClientVersion
	}
	return v
}

// Handler consumes what the agent sends: session/update notifications and
// agent→client requests. Both methods are called from the client's read loop
// goroutine (requests from their own goroutine, so a slow answer cannot block
// notifications), and must be safe for concurrent use.
type Handler interface {
	// SessionUpdate receives one session/update payload for a session.
	SessionUpdate(sessionID string, u Update)
	// RequestPermission answers an agent's session/request_permission.
	RequestPermission(req RequestPermissionParams) PermissionOutcome
}

// nopHandler is the Handler used before a turn installs one (and if it never
// does): updates are dropped and permissions are cancelled, which is the safe
// default — an unanswered tool call never runs.
type nopHandler struct{}

func (nopHandler) SessionUpdate(string, Update) {}

func (nopHandler) RequestPermission(RequestPermissionParams) PermissionOutcome {
	return PermissionCancelled()
}

// Client drives one ACP agent process: JSON-RPC 2.0 over its stdin/stdout, with
// concurrent-safe request/response correlation, notification dispatch and
// agent→client request handling.
//
// One Client owns exactly one process. It is safe for concurrent use, but a turn
// (Prompt) must not be started twice at once: the agent serves one prompt per
// session at a time.
type Client struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	enc   *codec
	rd    *reader
	tree  *proctree.Tree

	clientInfo Implementation

	mu      sync.Mutex
	pending map[string]chan *Message
	nextID  int64
	handler Handler
	closed  bool
	dead    error // transport failure: every later call fails with it
	deadCh  chan struct{}
}

// Start launches the agent process and begins reading its stdout. The returned
// client owns the process until Close.
func Start(_ context.Context, opts Options) (*Client, error) {
	if opts.Command == "" {
		return nil, errors.New("acp: Start: empty command")
	}
	cmd := exec.Command(opts.Command, opts.Args...)
	procattr.Background(cmd)
	cmd.Dir = opts.Dir
	cmd.Env = util.EnvironWithout(opts.EnvDeny, opts.EnvAllow, opts.Env)
	// F12: the agent's descendants must be killable as one unit, and Wait must not block
	// forever on a pipe their orphan holds.
	tree := proctree.New()
	tree.Configure(cmd)
	cmd.WaitDelay = waitDelay
	if opts.Stderr != nil {
		cmd.Stderr = opts.Stderr
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("acp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("acp: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("acp: start %q: %w", opts.Command, err)
	}
	if err := tree.Attach(cmd); err != nil {
		// Containment is a protection, not a precondition: without it Close degrades to
		// killing the direct child (the pre-F12 behaviour).
		slog.Warn("acp: cannot contain the agent's process tree", "pid", cmd.Process.Pid, "err", err)
	}

	info := Implementation{Name: "gofer", Version: defaultClientVersion()}
	if opts.ClientInfo != nil {
		info = *opts.ClientInfo
		if info.Name == "" {
			info.Name = "gofer"
		}
		if info.Version == "" {
			info.Version = defaultClientVersion()
		}
	}
	c := &Client{
		cmd:        cmd,
		stdin:      stdin,
		enc:        &codec{w: stdin},
		rd:         &reader{r: bufio.NewReader(stdout)},
		tree:       tree,
		clientInfo: info,
		pending:    map[string]chan *Message{},
		handler:    nopHandler{},
		deadCh:     make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// Done closes when the agent protocol stream ends, including an unexpected
// process exit while no prompt call is in flight.
func (c *Client) Done() <-chan struct{} { return c.deadCh }

// DeadErr explains why Done closed.
func (c *Client) DeadErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dead
}

// Initialize performs the ACP handshake. This client declares no fs/terminal
// capability (the agent uses its own tools), so the agent must not delegate them.
func (c *Client) Initialize(ctx context.Context) (InitializeResult, error) {
	params := InitializeParams{
		ProtocolVersion:    ProtocolVersion,
		ClientCapabilities: ClientCapabilities{Terminal: false},
		ClientInfo:         &c.clientInfo,
	}
	raw, err := c.call(ctx, MethodInitialize, params, nil)
	var out InitializeResult
	if len(raw) > 0 {
		if uerr := json.Unmarshal(raw, &out); uerr != nil && err == nil {
			err = fmt.Errorf("acp: decode initialize result: %w", uerr)
		}
	}
	if err != nil {
		return InitializeResult{}, err
	}
	if out.ProtocolVersion != ProtocolVersion {
		slog.Warn("acp: protocol version mismatch", "client", ProtocolVersion, "agent", out.ProtocolVersion)
	}
	return out, nil
}

// NewSession opens a session in cwd, advertising the given MCP servers (nil/empty
// for none). The response MUST carry the session id every later call needs: that is
// what session/new answers with (unlike session/load), so an empty one is an agent
// bug, not a shape to interpret.
func (c *Client) NewSession(ctx context.Context, params SessionNewParams) (SessionNewResult, error) {
	if params.MCPServers == nil {
		params.MCPServers = []MCPServer{}
	}
	out, err := c.sessionCall(ctx, MethodSessionNew, params)
	if err != nil {
		return SessionNewResult{}, err
	}
	if out.SessionID == "" {
		return SessionNewResult{}, fmt.Errorf("acp: %s returned an empty sessionId", MethodSessionNew)
	}
	return out, nil
}

// LoadSession resumes an existing session (S2 uses it): the agent replays that
// session's history so the next prompt continues it. The agent must have declared
// agentCapabilities.loadSession.
//
// The ACP schema's session/load response carries NO sessionId — what was loaded IS
// params.SessionID, and the result, when it carries anything, is the session's own
// state (modes/…) — so an empty result is a compliant answer and is attributed to
// the requested session. Requiring an id here failed every real resume with
// "session/load returned an empty sessionId" (ACP-02 真机, 2026-09-25: omp-acp and
// jcode-acp both answer this way). An agent that DOES answer with an id is a
// non-standard adapter: a differing id is warned about and then used, because the
// agent is the authority on what it actually loaded and the turn has to go there.
func (c *Client) LoadSession(ctx context.Context, params SessionLoadParams) (SessionNewResult, error) {
	if params.MCPServers == nil {
		params.MCPServers = []MCPServer{}
	}
	out, err := c.sessionCall(ctx, MethodSessionLoad, params)
	if err != nil {
		return SessionNewResult{}, err
	}
	switch {
	case out.SessionID == "":
		out.SessionID = params.SessionID
	case out.SessionID != params.SessionID:
		slog.Warn("acp: session/load answered with a different sessionId; using the agent's",
			"requested", params.SessionID, "response", out.SessionID)
	}
	return out, nil
}

// sessionCall issues a call whose result is a session id (+ optional modes) and
// decodes it. Whether the id may be empty is the caller's call: session/new must
// answer with one, session/load (by the schema) does not.
func (c *Client) sessionCall(ctx context.Context, method string, params any) (SessionNewResult, error) {
	raw, err := c.call(ctx, method, params, nil)
	var out SessionNewResult
	if len(raw) > 0 {
		if uerr := json.Unmarshal(raw, &out); uerr != nil && err == nil {
			err = fmt.Errorf("acp: decode %s result: %w", method, uerr)
		}
	}
	if err != nil {
		return SessionNewResult{}, err
	}
	return out, nil
}

// SetMode switches a session's agent mode (e.g. a read-only mode). S2 drives it
// from job --read-only.
func (c *Client) SetMode(ctx context.Context, sessionID, modeID string) error {
	_, err := c.call(ctx, MethodSessionSetMode, SessionSetModeParams{SessionID: sessionID, ModeID: modeID}, nil)
	return err
}

// Prompt runs one prompt turn: the text is sent as a single text content block and
// every session/update it triggers is delivered to h. The agent→client permission
// requests of the turn are answered by h too.
//
// Cancellation: when ctx ends, session/cancel is sent first and the client waits up
// to cancelGrace for the agent's stopReason cancelled response — so a cancelled
// turn still reports its stopReason — then returns the context error. The caller
// closes (and, if needed, kills) the process.
func (c *Client) Prompt(ctx context.Context, sessionID, text string, h Handler) (PromptResult, error) {
	c.setHandler(h)
	params := PromptParams{SessionID: sessionID, Prompt: []ContentBlock{{Type: "text", Text: text}}}
	raw, err := c.call(ctx, MethodSessionPrompt, params, func() {
		if cerr := c.Cancel(sessionID); cerr != nil {
			slog.Debug("acp: session/cancel failed", "session_id", sessionID, "err", cerr)
		}
	})
	var out PromptResult
	if len(raw) > 0 {
		if uerr := json.Unmarshal(raw, &out); uerr != nil && err == nil {
			err = fmt.Errorf("acp: decode session/prompt result: %w", uerr)
		}
	}
	return out, err
}

// Cancel tells the agent to abort the session's current turn. It is a
// notification: the agent answers by ending the in-flight prompt with stopReason
// cancelled.
func (c *Client) Cancel(sessionID string) error {
	return c.notify(MethodSessionCancel, SessionCancelParams{SessionID: sessionID})
}

// Close ends the client: stdin is closed (the agent's documented exit signal), then
// the process is given closeGrace to exit before its WHOLE PROCESS TREE is killed. It
// is idempotent.
//
// Every wait here is bounded (F12): the tree kill removes descendants that could
// keep cmd.Wait blocked on inherited stdio; waitDelay and waitAfterKill bound the
// fallback if a process refuses to be reaped.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	defer c.tree.Release()

	_ = c.stdin.Close()
	// stdin close lets the adapter flush, but descendants can outlive the direct
	// process even when Wait succeeds. Terminate the whole owned process group/job
	// object before reaping the leader.
	c.tree.Kill()
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case err := <-done:
		// err is exec.ErrWaitDelay when the agent exited but a descendant still held its
		// stdio: that descendant is exactly what must not survive this call.
		if errors.Is(err, exec.ErrWaitDelay) {
			c.tree.Kill()
		}
		return err
	case <-time.After(closeGrace):
		slog.Debug("acp: agent did not exit after stdin close, killing the tree", "pid", c.cmd.Process.Pid)
		c.tree.Kill()
		select {
		case err := <-done:
			return err
		case <-time.After(waitAfterKill):
			slog.Warn("acp: agent tree did not reap after kill", "pid", c.cmd.Process.Pid)
			return nil
		}
	}
}

// setHandler installs the handler for the turn about to start.
func (c *Client) setHandler(h Handler) {
	if h == nil {
		h = nopHandler{}
	}
	c.mu.Lock()
	c.handler = h
	c.mu.Unlock()
}

// handler returns the current handler (never nil).
func (c *Client) handlerFunc() Handler {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.handler
}

// notify writes a JSON-RPC notification (no id, no response expected).
func (c *Client) notify(method string, params any) error {
	rawParams, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("acp: encode %s params: %w", method, err)
	}
	return c.enc.write(&Message{JSONRPC: "2.0", Method: method, Params: rawParams})
}

// call writes a request and waits for its response. onCancel (nil-safe) runs once if
// ctx ends first; when it is non-nil the call then still waits cancelGrace for the
// response, so a cancelled TURN's stopReason is not lost. With no onCancel there is
// nothing to salvage and the call returns the context error AT ONCE (F12): a wedged
// agent must not delay the cancellation of the job waiting on it.
func (c *Client) call(ctx context.Context, method string, params any, onCancel func()) (json.RawMessage, error) {
	rawParams, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("acp: encode %s params: %w", method, err)
	}

	c.mu.Lock()
	if c.dead != nil {
		err := c.dead
		c.mu.Unlock()
		return nil, err
	}
	c.nextID++
	id := strconv.FormatInt(c.nextID, 10)
	ch := make(chan *Message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	req := &Message{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: rawParams}
	if err := c.enc.write(req); err != nil {
		c.forget(id)
		return nil, err
	}

	select {
	case msg := <-ch:
		return resultOf(msg)
	case <-ctx.Done():
		if onCancel == nil {
			c.forget(id)
			return nil, ctx.Err()
		}
		onCancel()
		select {
		case msg := <-ch:
			raw, rerr := resultOf(msg)
			if rerr == nil {
				return raw, ctx.Err()
			}
			return nil, ctx.Err()
		case <-time.After(cancelGrace):
			c.forget(id)
			return nil, ctx.Err()
		}
	}
}

// resultOf unwraps a response message.
func resultOf(msg *Message) (json.RawMessage, error) {
	if msg.Error != nil {
		return nil, msg.Error
	}
	return msg.Result, nil
}

// forget drops a pending call (its response, if it ever arrives, is ignored).
func (c *Client) forget(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// deliver routes a response to the goroutine waiting for it.
func (c *Client) deliver(msg *Message) {
	id := string(msg.ID)
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- msg
	}
}

// readLoop dispatches incoming messages until the protocol channel ends.
func (c *Client) readLoop() {
	for {
		msg, err := c.rd.read()
		if err != nil {
			c.fail(err)
			return
		}
		switch {
		case msg.isResponse():
			c.deliver(msg)
		case msg.isRequest():
			// Its own goroutine: answering a permission request may block on a human,
			// and that must not stop session/update notifications (or the response we
			// are waiting for) from being read.
			go c.handleRequest(msg)
		default:
			c.handleNotification(msg)
		}
	}
}

// fail records a terminal transport error and wakes every pending call with it.
func (c *Client) fail(err error) {
	if errors.Is(err, io.EOF) {
		err = errors.New("acp: agent closed the protocol channel")
	}
	c.mu.Lock()
	if c.dead == nil {
		c.dead = err
		close(c.deadCh)
	}
	pending := make([]chan *Message, 0, len(c.pending))
	for id, ch := range c.pending {
		pending = append(pending, ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- &Message{Error: &RPCError{Code: CodeInternalError, Message: err.Error()}}
	}
}

// handleNotification delivers a session/update to the current handler and drops
// anything else (an agent may send notifications this client does not model).
func (c *Client) handleNotification(msg *Message) {
	if msg.Method != MethodSessionUpdate {
		slog.Debug("acp: ignoring notification", "method", msg.Method)
		return
	}
	var p SessionUpdateParams
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		slog.Warn("acp: undecodable session/update", "err", err)
		return
	}
	c.handlerFunc().SessionUpdate(p.SessionID, p.Update)
}

// handleRequest answers an agent→client request. Only session/request_permission
// is implemented; fs/*, terminal/* (capabilities this client never declares) and
// every unknown method get JSON-RPC -32601 method not found.
func (c *Client) handleRequest(msg *Message) {
	switch msg.Method {
	case MethodRequestPermission:
		var p RequestPermissionParams
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			c.respondError(msg.ID, CodeInvalidParams, "invalid session/request_permission params")
			return
		}
		out := c.handlerFunc().RequestPermission(p)
		if out.Outcome != OutcomeSelected {
			out = PermissionCancelled()
		}
		c.respondResult(msg.ID, permissionResult{Outcome: out})
	default:
		c.respondError(msg.ID, CodeMethodNotFound, "method not found: "+msg.Method)
	}
}

// respondResult writes a successful response to an agent request.
func (c *Client) respondResult(id json.RawMessage, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		c.respondError(id, CodeInternalError, "failed to encode response")
		return
	}
	if err := c.enc.write(&Message{JSONRPC: "2.0", ID: id, Result: raw}); err != nil {
		slog.Debug("acp: write response failed", "err", err)
	}
}

// respondError writes a JSON-RPC error response to an agent request.
func (c *Client) respondError(id json.RawMessage, code int, message string) {
	if err := c.enc.write(&Message{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: message}}); err != nil {
		slog.Debug("acp: write error response failed", "err", err)
	}
}
