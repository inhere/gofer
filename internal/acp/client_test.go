package acp_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp"
	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// recorder is the test Handler: it merges agent message chunks the way the real
// runner does, records every update and permission request, and answers
// permissions with a scripted outcome.
type recorder struct {
	mu       sync.Mutex
	text     strings.Builder
	updates  []acp.Update
	perms    []acp.RequestPermissionParams
	answer   func(acp.RequestPermissionParams) acp.PermissionOutcome
	firstMsg chan struct{}
}

func newRecorder() *recorder {
	return &recorder{
		firstMsg: make(chan struct{}),
		answer: func(p acp.RequestPermissionParams) acp.PermissionOutcome {
			for _, o := range p.Options {
				if o.Kind == acp.OptionAllowOnce {
					return acp.PermissionSelected(o.OptionID)
				}
			}
			return acp.PermissionCancelled()
		},
	}
}

func (r *recorder) SessionUpdate(_ string, u acp.Update) {
	r.mu.Lock()
	r.updates = append(r.updates, u)
	if u.MessageChunk != nil && u.Kind == acp.UpdateAgentMessageChunk {
		r.text.WriteString(u.MessageChunk.Text)
	}
	first := len(r.updates) == 1
	r.mu.Unlock()
	if first {
		close(r.firstMsg)
	}
}

func (r *recorder) RequestPermission(p acp.RequestPermissionParams) acp.PermissionOutcome {
	r.mu.Lock()
	r.perms = append(r.perms, p)
	answer := r.answer
	r.mu.Unlock()
	return answer(p)
}

func (r *recorder) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.updates))
	for _, u := range r.updates {
		out = append(out, u.Kind)
	}
	return out
}

func (r *recorder) textSoFar() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.text.String()
}

func (r *recorder) snapshot() ([]acp.Update, []acp.RequestPermissionParams) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]acp.Update(nil), r.updates...), append([]acp.RequestPermissionParams(nil), r.perms...)
}

// startFake launches the in-repo fake ACP agent and returns a started client plus
// its captured stderr (the agent's own log channel).
func startFake(t *testing.T, o acptest.Options) (*acp.Client, *bytes.Buffer) {
	t.Helper()
	var stderr bytes.Buffer
	c, err := acp.Start(context.Background(), acp.Options{
		Command: testcmd.Path(t),
		Args:    acptest.CmdArgs(o),
		Dir:     t.TempDir(),
		Stderr:  &stderr,
	})
	if err != nil {
		t.Fatalf("acp.Start: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, &stderr
}

// handshake initializes the client and opens one session, returning the session id.
func handshake(t *testing.T, c *acp.Client, ctx context.Context) string {
	t.Helper()
	init, err := c.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if init.ProtocolVersion != 1 {
		t.Fatalf("protocolVersion = %d, want 1", init.ProtocolVersion)
	}
	if !init.AgentCapabilities.LoadSession {
		t.Fatalf("agentCapabilities.loadSession = false, want true: %+v", init.AgentCapabilities)
	}
	if init.AgentInfo == nil || init.AgentInfo.Name != "acptest" {
		t.Fatalf("agentInfo = %+v, want acptest", init.AgentInfo)
	}
	sess, err := c.NewSession(ctx, acp.SessionNewParams{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sess.SessionID != acptest.SessionID {
		t.Fatalf("sessionId = %q, want %q", sess.SessionID, acptest.SessionID)
	}
	if sess.Modes == nil || sess.Modes.CurrentModeID != acptest.ModeID || len(sess.Modes.AvailableModes) != 2 {
		t.Fatalf("modes = %+v, want current %q and 2 available modes", sess.Modes, acptest.ModeID)
	}
	return sess.SessionID
}

// TestACPClientHandshakeAndPrompt drives the whole S0 happy path against the fake
// agent: initialize capability negotiation, session/new, and one prompt turn whose
// session/update variants all reach the handler.
func TestACPClientHandshakeAndPrompt(t *testing.T) {
	c, _ := startFake(t, acptest.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sid := handshake(t, c, ctx)
	h := newRecorder()
	pr, err := c.Prompt(ctx, sid, "list the repo", h)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if pr.StopReason != acp.StopEndTurn {
		t.Fatalf("stopReason = %q, want %q", pr.StopReason, acp.StopEndTurn)
	}
	if got, want := h.textSoFar(), acptest.TextHello+acptest.TextWorld+acptest.TextDone; got != want {
		t.Fatalf("merged agent text = %q, want %q", got, want)
	}

	kinds := h.kinds()
	for _, want := range []string{
		acp.UpdateAgentMessageChunk, acp.UpdateAgentThoughtChunk,
		acp.UpdateToolCall, acp.UpdateToolCallUpdate,
		acp.UpdatePlan, acp.UpdateCurrentMode,
	} {
		if !contains(kinds, want) {
			t.Fatalf("update %q missing from %v", want, kinds)
		}
	}

	updates, _ := h.snapshot()
	var statuses []string
	for _, u := range updates {
		if u.ToolCall != nil {
			statuses = append(statuses, u.ToolCall.Status)
		}
	}
	if got, want := strings.Join(statuses, ","), "pending,in_progress,completed"; got != want {
		t.Fatalf("tool call statuses = %q, want %q", got, want)
	}

	var plan *acp.Plan
	for _, u := range updates {
		if u.Plan != nil {
			plan = u.Plan
		}
	}
	if plan == nil || len(plan.Entries) != 2 || plan.Entries[0].Priority != "high" {
		t.Fatalf("plan = %+v, want 2 entries with a priority", plan)
	}
}

// TestACPClientPermissionRoundTrip proves an agent→client session/request_permission
// reaches the handler with its options decoded and that the handler's selected
// optionId is what the agent receives back.
func TestACPClientPermissionRoundTrip(t *testing.T) {
	c, stderr := startFake(t, acptest.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sid := handshake(t, c, ctx)
	h := newRecorder()
	if _, err := c.Prompt(ctx, sid, "edit it", h); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	_, perms := h.snapshot()
	if len(perms) != 1 {
		t.Fatalf("permission requests = %d, want 1", len(perms))
	}
	p := perms[0]
	if p.SessionID != acptest.SessionID {
		t.Fatalf("permission sessionId = %q, want %q", p.SessionID, acptest.SessionID)
	}
	if p.ToolCall == nil || p.ToolCall.ToolCallID != acptest.ToolCallID || p.ToolCall.Kind != "edit" {
		t.Fatalf("permission toolCall = %+v, want the scripted edit call", p.ToolCall)
	}
	if len(p.Options) != 3 {
		t.Fatalf("permission options = %+v, want 3", p.Options)
	}
	if p.Options[0].Kind != acp.OptionAllowOnce || p.Options[0].OptionID != acptest.AllowOnceOptionID {
		t.Fatalf("first option = %+v, want allow_once/%s", p.Options[0], acptest.AllowOnceOptionID)
	}
	if p.Options[2].Kind != acp.OptionRejectOnce {
		t.Fatalf("third option = %+v, want reject_once", p.Options[2])
	}
	// The recorder's default answer is the allow_once option; the fake agent echoes
	// the outcome it received onto stderr.
	if !strings.Contains(stderr.String(), "permission outcome=selected option="+acptest.AllowOnceOptionID+" kind=allow_once") {
		t.Fatalf("fake agent did not receive the selected option:\n%s", stderr.String())
	}
}

// TestACPClientCancelYieldsCancelled proves the cancel contract: a cancelled ctx
// sends session/cancel as a notification, the in-flight session/prompt still
// answers with stopReason cancelled, and Prompt reports the context error.
func TestACPClientCancelYieldsCancelled(t *testing.T) {
	c, stderr := startFake(t, acptest.Options{Slow: true})
	ctx, cancelSetup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelSetup()

	sid := handshake(t, c, ctx)
	h := newRecorder()

	pctx, cancel := context.WithCancel(context.Background())
	type result struct {
		pr  acp.PromptResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		pr, err := c.Prompt(pctx, sid, "keep going", h)
		done <- result{pr, err}
	}()

	select {
	case <-h.firstMsg:
	case <-time.After(20 * time.Second):
		t.Fatal("no session/update reached the handler")
	}
	cancel()

	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("Prompt err = %v, want context.Canceled", got.err)
		}
		if got.pr.StopReason != acp.StopCancelled {
			t.Fatalf("stopReason = %q, want %q", got.pr.StopReason, acp.StopCancelled)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Prompt did not return after ctx cancellation")
	}
	if !strings.Contains(stderr.String(), "session/cancel received") {
		t.Fatalf("fake agent never saw session/cancel:\n%s", stderr.String())
	}
}

// TestACPClientRejectsFsAndTerminal proves the S0 capability boundary: an agent's
// fs/* and terminal/* requests (gofer declares neither) are answered -32601 method
// not found, like any unknown method.
func TestACPClientRejectsFsAndTerminal(t *testing.T) {
	c, stderr := startFake(t, acptest.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sid := handshake(t, c, ctx)
	if _, err := c.Prompt(ctx, sid, "read main.go", newRecorder()); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	for _, method := range []string{"fs/read_text_file", "terminal/create"} {
		if !strings.Contains(stderr.String(), "acptest: "+method+" rejected code=-32601") {
			t.Fatalf("%s was not rejected with -32601 by the client:\n%s", method, stderr.String())
		}
	}
}

// TestACPClientLoadSessionRefused proves the interface S2 will use is wired: a
// session/load against an agent that does not declare loadSession surfaces the
// agent's JSON-RPC error to the caller.
func TestACPClientLoadSessionRefused(t *testing.T) {
	c, _ := startFake(t, acptest.Options{RefuseLoad: true})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	_, err := c.LoadSession(ctx, acp.SessionLoadParams{SessionID: acptest.SessionID, Cwd: t.TempDir()})
	if err == nil {
		t.Fatal("LoadSession succeeded against an agent without loadSession")
	}
	if !strings.Contains(err.Error(), "-32601") {
		t.Fatalf("LoadSession err = %v, want the agent's -32601", err)
	}
}

// contains reports whether s holds want.
func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
