// Package acptest provides an in-repo fake ACP agent: a scripted JSON-RPC 2.0
// stdio server used as a test double for internal/acp and internal/runner/acp.
//
// It is deliberately self-contained (stdlib only, its own framing) rather than
// built on internal/acp's codec, so a test that drives the fake server exercises
// the client against an INDEPENDENT encoder: a framing bug cannot cancel itself
// out on both sides.
//
// The binary entry point is the repo's testcmd program (`gofer-testcmd acp-fake
// <flags>`); tests build the argv with Args.
package acptest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SessionID is the session id the fake server returns from session/new.
const SessionID = "sess-acptest-1"

// Scripted agent text. Chunks are emitted separately to prove the client merges
// consecutive agent_message_chunk updates into one text stream.
const (
	TextHello  = "Hello"
	TextWorld  = " world."
	TextDone   = " Done."
	TextThink  = "pondering the request"
	ToolCallID = "tc-1"
	ToolTitle  = "Read main.go"
	ToolKind   = "read"
	ModeID     = "default"
	ReadOnlyID = "read-only"
)

// Options scripts the fake server.
type Options struct {
	// StopReason is the session/prompt stopReason to answer with. Empty means
	// end_turn.
	StopReason string
	// Slow makes the prompt turn answer only after session/cancel arrives (the
	// agent ignores the cancel itself and reports stopReason "cancelled").
	Slow bool
	// RefuseLoad makes session/load fail with -32601 (the agent has no loadSession).
	RefuseLoad bool
	// Delay is inserted before the final prompt response (lets a test cancel a
	// turn that is otherwise about to finish).
	Delay time.Duration
	// PermissionKind is the toolCall.kind of the session/request_permission the turn
	// raises ("" => PermissionKindDefault, "edit"). A "read"-ish kind is what the
	// approval gate's auto_allow_kinds is about.
	PermissionKind string
	// PermissionOptions lists the option KINDS the request offers, in order
	// ("" => allow_once, allow_always, reject_once — the S0 script). Dropping
	// reject_once is how a test reaches "rejected with no reject option available".
	PermissionOptions []string
	// PermissionRepeats is how many times the turn asks for permission (0 => 1). Every
	// ask repeats the SAME tool call and options, so an approval policy can be watched
	// across consecutive asks (e.g. allow_always remembering a kind).
	PermissionRepeats int
}

// Main runs the fake server over stdin/stdout. It returns the process exit code.
func Main(args []string) int {
	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "acptest:", err)
		return 2
	}
	s := newServer(opts, os.Stdin, os.Stdout, os.Stderr)
	s.serve()
	return 0
}

// parseArgs decodes the `acp-fake` flags.
func parseArgs(args []string) (Options, error) {
	var o Options
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--stop-reason":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--stop-reason needs a value")
			}
			i++
			o.StopReason = args[i]
		case "--slow":
			o.Slow = true
		case "--refuse-load":
			o.RefuseLoad = true
		case "--delay":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--delay needs a value")
			}
			i++
			d, err := time.ParseDuration(args[i])
			if err != nil {
				return o, fmt.Errorf("--delay: %w", err)
			}
			o.Delay = d
		case "--perm-kind":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--perm-kind needs a value")
			}
			i++
			o.PermissionKind = args[i]
		case "--perm-options":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--perm-options needs a value")
			}
			i++
			o.PermissionOptions = strings.Split(args[i], ",")
			for _, k := range o.PermissionOptions {
				if _, ok := permissionOptionID[k]; !ok {
					return o, fmt.Errorf("--perm-options: unknown option kind %q", k)
				}
			}
		case "--perm-repeats":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--perm-repeats needs a value")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				return o, fmt.Errorf("--perm-repeats: want a non-negative integer, got %q", args[i])
			}
			o.PermissionRepeats = n
		default:
			return o, fmt.Errorf("unknown flag %q", args[i])
		}
	}
	return o, nil
}

// server is the fake agent. Writes are serialised (one JSON message per line) and
// the read loop stays in the calling goroutine while a scripted prompt turn runs
// in its own goroutine, mirroring a real agent that keeps reading stdin.
type server struct {
	opts   Options
	in     *bufio.Reader
	out    io.Writer
	errOut io.Writer

	mu      sync.Mutex
	pending map[string]chan *rpcMsg
	nextID  int

	turnMu   sync.Mutex
	turnStop chan struct{} // closed when the in-flight prompt turn should stop
}

func newServer(opts Options, in io.Reader, out, errOut io.Writer) *server {
	return &server{
		opts:    opts,
		in:      bufio.NewReader(in),
		out:     out,
		errOut:  errOut,
		pending: map[string]chan *rpcMsg{},
	}
}

// rpcMsg is one JSON-RPC 2.0 message on the wire.
type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *server) serve() {
	for {
		line, err := s.in.ReadBytes('\n')
		if len(line) > 0 {
			s.dispatch(line)
		}
		if err != nil {
			return
		}
	}
}

func (s *server) dispatch(line []byte) {
	var msg rpcMsg
	if err := json.Unmarshal(line, &msg); err != nil {
		fmt.Fprintln(s.errOut, "acptest: bad message:", err)
		return
	}
	switch {
	case msg.Method != "" && len(msg.ID) > 0:
		s.handleRequest(&msg)
	case msg.Method != "":
		s.handleNotification(&msg)
	case len(msg.ID) > 0:
		s.deliver(&msg)
	}
}

func (s *server) handleRequest(msg *rpcMsg) {
	switch msg.Method {
	case "initialize":
		var p struct {
			ClientInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		fmt.Fprintf(s.errOut, "acptest: initialize client=%s/%s\n", p.ClientInfo.Name, p.ClientInfo.Version)
		s.reply(msg.ID, map[string]any{
			"protocolVersion": 1,
			"agentCapabilities": map[string]any{
				"loadSession":        !s.opts.RefuseLoad,
				"promptCapabilities": map[string]any{"image": false, "audio": false},
				"mcpCapabilities":    map[string]any{"http": false, "sse": false},
			},
			"authMethods": []any{},
			"agentInfo":   map[string]any{"name": "acptest", "version": "0.1.0"},
		})
	case "session/new":
		var p struct {
			Cwd        string `json:"cwd"`
			MCPServers []struct {
				Name string `json:"name"`
			} `json:"mcpServers"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		fmt.Fprintf(s.errOut, "acptest: session/new cwd=%s mcp_servers=%d\n", p.Cwd, len(p.MCPServers))
		s.reply(msg.ID, map[string]any{
			"sessionId": SessionID,
			"modes": map[string]any{
				"currentModeId": ModeID,
				"availableModes": []any{
					map[string]any{"id": ModeID, "name": "Default"},
					map[string]any{"id": ReadOnlyID, "name": "Read-only"},
				},
			},
		})
	case "session/load":
		if s.opts.RefuseLoad {
			s.replyError(msg.ID, -32601, "method not found: session/load")
			return
		}
		s.reply(msg.ID, map[string]any{"sessionId": SessionID})
	case "session/set_mode":
		var p struct {
			ModeID string `json:"modeId"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		fmt.Fprintf(s.errOut, "acptest: session/set_mode mode=%s\n", p.ModeID)
		s.reply(msg.ID, map[string]any{})
	case "session/prompt":
		s.startTurn(msg)
	default:
		s.replyError(msg.ID, -32601, "method not found: "+msg.Method)
	}
}

func (s *server) handleNotification(msg *rpcMsg) {
	switch msg.Method {
	case "session/cancel":
		fmt.Fprintln(s.errOut, "acptest: session/cancel received")
		s.stopTurn()
	case "session/update":
		// A client never sends this; ignore.
	default:
		fmt.Fprintf(s.errOut, "acptest: unexpected notification %s\n", msg.Method)
	}
}

// startTurn runs the scripted prompt turn in the background so the read loop can
// still receive session/cancel (and the client's responses to our requests).
func (s *server) startTurn(msg *rpcMsg) {
	s.turnMu.Lock()
	if s.turnStop != nil {
		s.turnMu.Unlock()
		s.replyError(msg.ID, -32602, "a prompt turn is already in flight")
		return
	}
	stop := make(chan struct{})
	s.turnStop = stop
	s.turnMu.Unlock()

	go func() {
		defer func() {
			s.turnMu.Lock()
			s.turnStop = nil
			s.turnMu.Unlock()
		}()
		s.runTurn(msg, stop)
	}()
}

// stopTurn signals the in-flight turn (if any) to wind up with stopReason
// cancelled.
func (s *server) stopTurn() {
	s.turnMu.Lock()
	stop := s.turnStop
	s.turnMu.Unlock()
	if stop != nil {
		close(stop)
	}
}

// runTurn is the scripted turn: message chunks, a thought, a tool call through its
// three statuses, a permission round trip, fs/terminal requests we expect the
// client to refuse, a plan, a mode update, and finally the prompt response.
func (s *server) runTurn(msg *rpcMsg, stop chan struct{}) {
	s.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": TextHello}})
	s.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": TextWorld}})
	s.update(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": TextThink}})

	// The client declared no fs/terminal capability: both must come back -32601.
	s.expectRefusal("fs/read_text_file", map[string]any{"sessionId": SessionID, "path": "main.go"})
	s.expectRefusal("terminal/create", map[string]any{"sessionId": SessionID, "command": "ls"})

	s.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": ToolCallID, "title": ToolTitle, "kind": ToolKind, "status": "pending", "rawInput": map[string]any{"path": "main.go"}})
	s.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": ToolCallID, "status": "in_progress"})
	s.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": ToolCallID, "status": "completed", "rawOutput": map[string]any{"lines": 42}})

	if outcome := s.requestPermission(); outcome != nil && strings.HasPrefix(outcome.optionKind, "reject") {
		fmt.Fprintf(s.errOut, "acptest: permission rejected (%s), stopping\n", outcome.optionKind)
		s.reply(msg.ID, map[string]any{"stopReason": "refusal"})
		return
	}

	s.update(map[string]any{"sessionUpdate": "plan", "entries": []any{
		map[string]any{"content": "read the repo", "priority": "high", "status": "completed"},
		map[string]any{"content": "explain it", "priority": "medium", "status": "pending"},
	}})
	s.update(map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": ModeID})

	if s.opts.Delay > 0 {
		select {
		case <-time.After(s.opts.Delay):
		case <-stop:
		}
	}
	if s.opts.Slow {
		select {
		case <-stop:
			fmt.Fprintln(s.errOut, "acptest: turn stopped by cancel")
			s.reply(msg.ID, map[string]any{"stopReason": "cancelled"})
			return
		case <-time.After(30 * time.Second):
		}
	}
	s.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": TextDone}})

	reason := s.opts.StopReason
	if reason == "" {
		reason = "end_turn"
	}
	s.reply(msg.ID, map[string]any{"stopReason": reason})
}

// permOutcome is what the client answered to our permission request.
type permOutcome struct {
	optionID   string
	optionKind string
	cancelled  bool
}

// requestPermission asks the client to approve a tool call — opts.PermissionRepeats
// times in a row (0/1 = once), each ask identical so a policy can be watched across
// consecutive asks. It returns the LAST outcome (what runTurn's reject branch keys on).
func (s *server) requestPermission() *permOutcome {
	n := s.opts.PermissionRepeats
	if n < 1 {
		n = 1
	}
	var last *permOutcome
	for range n {
		last = s.requestPermissionOnce()
	}
	return last
}

// permissionOptionID maps an ACP option kind to the optionId the fake agent offers
// for it; permissionOptionName is the matching human label.
var permissionOptionID = map[string]string{
	"allow_once":    AllowOnceOptionID,
	"allow_always":  AllowAlwaysOptionID,
	"reject_once":   RejectOnceOptionID,
	"reject_always": RejectAlwaysOptionID,
}

var permissionOptionName = map[string]string{
	"allow_once":    "Allow once",
	"allow_always":  "Always allow",
	"reject_once":   "Reject",
	"reject_always": "Always reject",
}

// permissionToolCall is the scripted tool call the request is about: the kind is
// scriptable (a "read" ask must be auto-allowed by the approval gate, an "edit" ask
// must reach a human), the id/title/rawInput stay fixed so a test can assert them.
func (s *server) permissionToolCall() map[string]any {
	kind := s.opts.PermissionKind
	if kind == "" {
		kind = PermissionKindDefault
	}
	return map[string]any{
		"toolCallId": ToolCallID,
		"title":      PermissionTitle,
		"kind":       kind,
		"status":     "pending",
		"rawInput":   map[string]any{"path": "main.go"},
		"locations":  []any{map[string]any{"path": "main.go"}},
	}
}

// requestPermissionOnce performs one session/request_permission round trip and
// records the options it offered plus the answer it got (the test asserts on both).
func (s *server) requestPermissionOnce() *permOutcome {
	kinds := s.opts.PermissionOptions
	if len(kinds) == 0 {
		kinds = []string{"allow_once", "allow_always", "reject_once"}
	}
	options := make([]any, 0, len(kinds))
	for _, k := range kinds {
		options = append(options, map[string]any{
			"optionId": permissionOptionID[k], "name": permissionOptionName[k], "kind": k,
		})
	}
	params := map[string]any{
		"sessionId": SessionID,
		"toolCall":  s.permissionToolCall(),
		"options":   options,
	}
	raw, err := s.call("session/request_permission", params)
	if err != nil {
		fmt.Fprintln(s.errOut, "acptest: permission call failed:", err)
		return nil
	}
	var res struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		fmt.Fprintln(s.errOut, "acptest: bad permission result:", err)
		return nil
	}
	out := &permOutcome{optionID: res.Outcome.OptionID, cancelled: res.Outcome.Outcome != "selected"}
	switch out.optionID {
	case "allow-once-id":
		out.optionKind = "allow_once"
	case "allow-always-id":
		out.optionKind = "allow_always"
	case "reject-once-id":
		out.optionKind = "reject_once"
	case "reject-always-id":
		out.optionKind = "reject_always"
	}
	fmt.Fprintf(s.errOut, "acptest: permission outcome=%s option=%s kind=%s\n", res.Outcome.Outcome, out.optionID, out.optionKind)
	return out
}

// expectRefusal sends a client-directed request the client must reject with
// -32601, and reports the code it received on stderr so a test can assert it.
func (s *server) expectRefusal(method string, params any) {
	raw, err := s.call(method, params)
	if err != nil {
		fmt.Fprintf(s.errOut, "acptest: %s rejected code=%s\n", method, rpcErrCode(err))
		return
	}
	fmt.Fprintf(s.errOut, "acptest: %s unexpectedly succeeded: %s\n", method, raw)
}

// rpcErrCode extracts the JSON-RPC error code from a *rpcError returned by call.
func rpcErrCode(err error) string {
	var re *rpcError
	if e, ok := err.(*rpcCallError); ok && e.rpc != nil {
		re = e.rpc
	}
	if re == nil {
		return "none"
	}
	return strconv.Itoa(re.Code)
}

// rpcCallError carries a JSON-RPC error response as an error value.
type rpcCallError struct {
	rpc *rpcError
}

func (e *rpcCallError) Error() string {
	return "jsonrpc error " + strconv.Itoa(e.rpc.Code) + ": " + e.rpc.Message
}

// call sends a request to the client and waits for its response.
func (s *server) call(method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	s.nextID++
	id := strconv.Itoa(1000 + s.nextID)
	ch := make(chan *rpcMsg, 1)
	s.pending[idKey(json.RawMessage(strconv.Quote(id)))] = ch
	s.mu.Unlock()

	rawParams, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	if err := s.write(&rpcMsg{JSONRPC: "2.0", ID: json.RawMessage(strconv.Quote(id)), Method: method, Params: rawParams}); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, &rpcCallError{rpc: resp.Error}
		}
		return resp.Result, nil
	case <-time.After(20 * time.Second):
		return nil, fmt.Errorf("timeout waiting for %s response", method)
	}
}

// deliver routes a response to the goroutine waiting on it.
func (s *server) deliver(msg *rpcMsg) {
	key := idKey(msg.ID)
	s.mu.Lock()
	ch := s.pending[key]
	delete(s.pending, key)
	s.mu.Unlock()
	if ch != nil {
		ch <- msg
	}
}

// idKey normalises a JSON-RPC id to its unquoted form: a string id arrives as
// `"1001"` and a numeric one as `1`, and both must key the same pending map.
func idKey(raw json.RawMessage) string {
	return strings.Trim(string(raw), `"`)
}

func (s *server) update(update map[string]any) {
	params, err := json.Marshal(map[string]any{"sessionId": SessionID, "update": update})
	if err != nil {
		return
	}
	_ = s.write(&rpcMsg{JSONRPC: "2.0", Method: "session/update", Params: params})
}

func (s *server) reply(id json.RawMessage, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		return
	}
	_ = s.write(&rpcMsg{JSONRPC: "2.0", ID: id, Result: raw})
}

func (s *server) replyError(id json.RawMessage, code int, message string) {
	_ = s.write(&rpcMsg{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

// write emits one newline-terminated JSON message. Serialised: notifications from
// the turn goroutine and responses from the read loop must not interleave bytes.
func (s *server) write(msg *rpcMsg) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.out.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}
