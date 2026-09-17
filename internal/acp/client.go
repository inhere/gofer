package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// cancelGrace bounds how long Prompt waits for the agent's stopReason cancelled
// response after session/cancel was sent. When it expires the client returns the
// context error anyway; the caller then kills the process (Close).
const cancelGrace = 10 * time.Second

// closeGrace bounds how long Close waits for the agent to exit after its stdin is
// closed before killing it.
const closeGrace = 3 * time.Second

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
	// Stderr receives the agent's own diagnostics verbatim (the protocol channel is
	// stdin/stdout only). Nil discards them.
	Stderr io.Writer
	// ClientInfo identifies this client in initialize. Nil means {Name: "gofer"}.
	ClientInfo *Implementation
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

	clientInfo Implementation

	mu      sync.Mutex
	pending map[string]chan *Message
	nextID  int64
	handler Handler
	closed  bool
	dead    error // transport failure: every later call fails with it
}

// Start launches the agent process and begins reading its stdout. The returned
// client owns the process until Close.
func Start(_ context.Context, opts Options) (*Client, error) {
	if opts.Command == "" {
		return nil, errors.New("acp: Start: empty command")
	}
	cmd := exec.Command(opts.Command, opts.Args...)
	cmd.Dir = opts.Dir
	cmd.Env = mergedEnv(opts.Env)
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

	info := Implementation{Name: "gofer"}
	if opts.ClientInfo != nil {
		info = *opts.ClientInfo
	}
	c := &Client{
		cmd:        cmd,
		stdin:      stdin,
		enc:        &codec{w: stdin},
		rd:         &reader{r: bufio.NewReader(stdout)},
		clientInfo: info,
		pending:    map[string]chan *Message{},
		handler:    nopHandler{},
	}
	go c.readLoop()
	return c, nil
}

// mergedEnv returns os.Environ() with extra layered on top.
func mergedEnv(extra map[string]string) []string {
	base := os.Environ()
	if len(extra) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(extra))
	out = append(out, base...)
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
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
// for none). The response carries the session id every later call needs.
func (c *Client) NewSession(ctx context.Context, params SessionNewParams) (SessionNewResult, error) {
	if params.MCPServers == nil {
		params.MCPServers = []MCPServer{}
	}
	return c.sessionCall(ctx, MethodSessionNew, params)
}

// LoadSession resumes an existing session (S2 uses it; S0 only carries the
// interface). The agent must have declared agentCapabilities.loadSession.
func (c *Client) LoadSession(ctx context.Context, params SessionLoadParams) (SessionNewResult, error) {
	if params.MCPServers == nil {
		params.MCPServers = []MCPServer{}
	}
	return c.sessionCall(ctx, MethodSessionLoad, params)
}

// sessionCall issues a call whose result is a session id (+ optional modes).
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
	if out.SessionID == "" {
		return SessionNewResult{}, fmt.Errorf("acp: %s returned an empty sessionId", method)
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

// Close ends the client: stdin is closed (the agent's documented exit signal),
// then the process is given closeGrace to exit before being killed. It is
// idempotent.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	_ = c.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(closeGrace):
		slog.Debug("acp: agent did not exit after stdin close, killing", "pid", c.cmd.Process.Pid)
		_ = c.cmd.Process.Kill()
		<-done
		return nil
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

// call writes a request and waits for its response. onCancel (nil-safe) runs once
// if ctx ends first; the call then still waits cancelGrace for the response so a
// cancelled turn's stopReason is not lost.
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
		if onCancel != nil {
			onCancel()
		}
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
