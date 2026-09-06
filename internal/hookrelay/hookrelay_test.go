package hookrelay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/client"
)

// fakeAPI is an in-memory hub for the runner tests.
type fakeAPI struct {
	mu        sync.Mutex
	sessions  map[string]client.AgentSession
	turns     map[string]client.Decision
	registers int
	beats     []client.SessionHeartbeat
	waits     int
	// scripted behaviour
	failHeartbeat error
	failWaitTimes int
	answerAfter   int // waits before the turn is answered
	answer        string
	relayOffAfter int // waits before relay flips off
}

func newFake() *fakeAPI {
	return &fakeAPI{sessions: map[string]client.AgentSession{}, turns: map[string]client.Decision{}}
}

func (f *fakeAPI) RegisterSession(in client.SessionRegister) (client.AgentSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registers++
	a := f.sessions[in.SessionID]
	a.SessionID, a.Agent, a.Cwd, a.Runner, a.State = in.SessionID, in.Agent, in.Cwd, in.Runner, "running"
	f.sessions[in.SessionID] = a
	return a, nil
}

func (f *fakeAPI) HeartbeatSession(sid string, hb client.SessionHeartbeat) (client.AgentSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failHeartbeat != nil {
		return client.AgentSession{}, f.failHeartbeat
	}
	a, ok := f.sessions[sid]
	if !ok {
		return client.AgentSession{}, &client.StatusError{Status: 404, Msg: "server 404: unknown session"}
	}
	f.beats = append(f.beats, hb)
	if hb.LastMessage != "" {
		a.LastMessage = hb.LastMessage
	}
	if hb.Title != "" && a.Title == "" {
		a.Title = hb.Title
	}
	f.sessions[sid] = a
	return a, nil
}

func (f *fakeAPI) OpenSessionTurn(sid, msg string, timeoutSec int64) (client.Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.sessions[sid]
	if !a.Relay {
		return client.Decision{}, &client.StatusError{Status: 409, Msg: "relay off"}
	}
	d := client.Decision{ID: "dec-1", Question: msg, State: "OPEN", TimeoutSec: timeoutSec, SessionID: sid, Kind: "relay"}
	f.turns[d.ID] = d
	return d, nil
}

func (f *fakeAPI) WaitSessionTurn(sid, id string, waitSec int) (client.TurnStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waits++
	if f.failWaitTimes > 0 {
		f.failWaitTimes--
		return client.TurnStatus{}, &client.StatusError{Status: 502, Msg: "bad gateway"}
	}
	d := f.turns[id]
	a := f.sessions[sid]
	if f.relayOffAfter > 0 && f.waits >= f.relayOffAfter {
		a.Relay = false
		f.sessions[sid] = a
		d.State = "EXPIRED"
		return client.TurnStatus{Outcome: "relay_off", Decision: d}, nil
	}
	if f.answerAfter > 0 && f.waits >= f.answerAfter {
		d.State, d.Answer = "ANSWERED", f.answer
		f.turns[id] = d
		return client.TurnStatus{Outcome: "answered", Relay: true, Decision: d}, nil
	}
	return client.TurnStatus{Outcome: "open", Relay: true, Decision: d}, nil
}

func (f *fakeAPI) SetSessionRelay(sid string, on bool) (client.AgentSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.sessions[sid]
	a.Relay = on
	f.sessions[sid] = a
	return a, nil
}

func payload(t *testing.T, agent string, m map[string]any) Payload {
	t.Helper()
	b, _ := json.Marshal(m)
	p, err := ParsePayload(agent, strings.NewReader(string(b)))
	assert.NoErr(t, err)
	return p
}

func fastOpts(log *strings.Builder) Options {
	return Options{Wait: 3 * time.Second, PollSec: 1, Log: log, sleep: func(time.Duration) {}}
}

func TestParsePayload(t *testing.T) {
	_, err := ParsePayload("gemini", strings.NewReader(`{}`))
	assert.Err(t, err)
	_, err = ParsePayload("claude", strings.NewReader(``))
	assert.Err(t, err)
	_, err = ParsePayload("claude", strings.NewReader(`{"hook_event_name":"Stop"}`))
	assert.Err(t, err)
	p := payload(t, "Codex", map[string]any{
		"session_id": "s1", "hook_event_name": "Stop", "cwd": "/w", "turn_id": 7,
		"last_assistant_message": "done", "stop_hook_active": true, "unknown_field": []int{1},
	})
	assert.Eq(t, "codex", p.Agent)
	assert.Eq(t, "done", p.LastAssistantMessage)
	assert.True(t, p.StopHookActive)
}

func TestRunSessionStartAndPromptRegisterThenBeat(t *testing.T) {
	f := newFake()
	var log strings.Builder
	cur := filepath.Join(t.TempDir(), "run", "sessions", "abc")
	opts := fastOpts(&log)
	opts.CurrentFile = cur
	opts.Runner = "w-1"
	res, err := Run(f, payload(t, "claude", map[string]any{"session_id": "s1", "hook_event_name": "SessionStart", "cwd": "/w/repo"}), opts)
	assert.NoErr(t, err)
	assert.False(t, res.Blocked)
	assert.Eq(t, 1, f.registers)
	assert.Eq(t, "w-1", f.sessions["s1"].Runner)
	b, _ := os.ReadFile(cur)
	assert.Eq(t, "s1\n", string(b))

	// Unknown session on heartbeat → register + retry.
	_, err = Run(f, payload(t, "claude", map[string]any{"session_id": "s2", "hook_event_name": "UserPromptSubmit", "cwd": "/w/repo", "prompt": "fix the flaky test\nmore"}), opts)
	assert.NoErr(t, err)
	assert.Eq(t, 2, f.registers)
	assert.Eq(t, "repo: fix the flaky test", f.sessions["s2"].Title)

	// Unknown event → no-op.
	_, err = Run(f, payload(t, "claude", map[string]any{"session_id": "s1", "hook_event_name": "PreToolUse"}), opts)
	assert.NoErr(t, err)
	assert.True(t, strings.Contains(log.String(), "ignored"))
}

func TestRunStopRelayOffReleases(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", Relay: false}
	var log strings.Builder
	res, err := Run(f, payload(t, "codex", map[string]any{"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "what next?"}), fastOpts(&log))
	assert.NoErr(t, err)
	assert.False(t, res.Blocked)
	assert.Eq(t, 0, f.waits)
	assert.Eq(t, "what next?", f.sessions["s1"].LastMessage)
	assert.True(t, strings.Contains(log.String(), "relay off"))
}

func TestRunStopAnsweredBlocks(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", Relay: true}
	f.answerAfter, f.answer = 2, "  use plan B  "
	var log strings.Builder
	res, err := Run(f, payload(t, "codex", map[string]any{"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "A or B?"}), fastOpts(&log))
	assert.NoErr(t, err)
	assert.True(t, res.Blocked)
	assert.Eq(t, ReplyPrefix+"use plan B", res.Reason)
	assert.Eq(t, "A or B?", f.turns["dec-1"].Question)
	var out map[string]string
	assert.NoErr(t, json.Unmarshal(BlockJSON(res.Reason), &out))
	assert.Eq(t, "block", out["decision"])
}

func TestRunStopOffCommandAndRelayOffAndTransient(t *testing.T) {
	// /off → relay switched off, not blocked.
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", Relay: true}
	f.answerAfter, f.answer = 1, "/OFF"
	var log strings.Builder
	res, _ := Run(f, payload(t, "codex", map[string]any{"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "x"}), fastOpts(&log))
	assert.False(t, res.Blocked)
	assert.False(t, f.sessions["s1"].Relay)

	// relay flipped off from the web while waiting → released.
	f = newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", Relay: true}
	f.relayOffAfter = 2
	res, _ = Run(f, payload(t, "codex", map[string]any{"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "x"}), fastOpts(&log))
	assert.False(t, res.Blocked)

	// transient wait failures are retried, then the answer still lands.
	f = newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", Relay: true}
	f.failWaitTimes, f.answerAfter, f.answer = 2, 3, "ok"
	res, _ = Run(f, payload(t, "codex", map[string]any{"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "x"}), fastOpts(&log))
	assert.True(t, res.Blocked)

	// hub unreachable on heartbeat → quiet release, no wait.
	f = newFake()
	f.failHeartbeat = &client.StatusError{Status: 0, Msg: "dial tcp: refused"}
	res, err := Run(f, payload(t, "codex", map[string]any{"session_id": "s1", "hook_event_name": "Stop"}), fastOpts(&log))
	assert.NoErr(t, err)
	assert.False(t, res.Blocked)
	assert.Eq(t, 0, f.waits)
}

func TestRunStopBudgetExhausted(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", Relay: true}
	var log strings.Builder
	opts := fastOpts(&log)
	opts.Wait = 1 * time.Second
	res, _ := Run(f, payload(t, "codex", map[string]any{"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "x"}), opts)
	assert.False(t, res.Blocked)
	assert.True(t, strings.Contains(log.String(), "budget exhausted"))
	last := f.beats[len(f.beats)-1]
	assert.Eq(t, "idle", last.State)
}

func TestLastAssistantText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"first"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done, "},{"type":"text","text":"what next?"}]}}`,
		`{"type":"summary","summary":"x"}`,
		`not json`,
		``,
	}
	assert.NoErr(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644))
	got, err := LastAssistantText(path, 0)
	assert.NoErr(t, err)
	assert.Eq(t, "done,\n\nwhat next?", got)

	got, err = LastAssistantText(path, 6)
	assert.NoErr(t, err)
	assert.Eq(t, "done,\n…", got)

	// string content + role-only shape
	assert.NoErr(t, os.WriteFile(path, []byte(`{"message":{"role":"assistant","content":"plain text"}}`+"\n"), 0o644))
	got, _ = LastAssistantText(path, 0)
	assert.Eq(t, "plain text", got)

	// nothing assistant → ""
	assert.NoErr(t, os.WriteFile(path, []byte(`{"type":"user"}`+"\n"), 0o644))
	got, _ = LastAssistantText(path, 0)
	assert.Eq(t, "", got)

	_, err = LastAssistantText(filepath.Join(dir, "missing.jsonl"), 0)
	assert.Err(t, err)

	// Large file: only the tail window is scanned, partial first line dropped.
	var big strings.Builder
	for i := 0; i < 20000; i++ {
		big.WriteString(`{"type":"user","message":{"role":"user","content":"padding padding padding padding padding"}}` + "\n")
	}
	big.WriteString(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"tail"}]}}` + "\n")
	assert.NoErr(t, os.WriteFile(path, []byte(big.String()), 0o644))
	got, _ = LastAssistantText(path, 0)
	assert.Eq(t, "tail", got)
}

func TestInstallMergeIdempotentAndRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	assert.NoErr(t, os.MkdirAll(filepath.Dir(path), 0o755))
	existing := `{
  "permissions": {"allow": ["Bash(ls)"]},
  "env": {"CLAUDE_CODE_STOP_HOOK_BLOCK_CAP": "9"},
  "hooks": {
    "SessionStart": [{"matcher": "", "hooks": [{"type": "command", "command": "bd prime"}]}],
    "PreCompact":   [{"matcher": "", "hooks": [{"type": "command", "command": "bd prime"}]}]
  }
}`
	assert.NoErr(t, os.WriteFile(path, []byte(existing), 0o644))

	res, err := Install(AgentClaude, path, false, false)
	assert.NoErr(t, err)
	assert.Eq(t, 5, res.Added)
	assert.Eq(t, 0, res.Replaced)
	assert.Len(t, res.Notes, 1) // env kept

	var doc map[string]any
	read := func() {
		b, err := os.ReadFile(path)
		assert.NoErr(t, err)
		assert.NoErr(t, json.Unmarshal(b, &doc))
	}
	read()
	assert.NotNil(t, doc["permissions"])
	env := doc["env"].(map[string]any)
	assert.Eq(t, "9", env["CLAUDE_CODE_STOP_HOOK_BLOCK_CAP"])
	hooks := doc["hooks"].(map[string]any)
	ss := hooks["SessionStart"].([]any)
	assert.Len(t, ss, 2) // bd + gofer
	assert.Len(t, hooks["PreCompact"].([]any), 1)
	stop := hooks["Stop"].([]any)
	assert.Len(t, stop, 1)
	cmd := stop[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"]
	assert.Eq(t, "gofer hook claude --wait 7140", cmd)

	// Idempotent: second install replaces, no duplicates.
	res, err = Install(AgentClaude, path, false, false)
	assert.NoErr(t, err)
	assert.Eq(t, 5, res.Added)
	assert.Eq(t, 5, res.Replaced)
	read()
	hooks = doc["hooks"].(map[string]any)
	assert.Len(t, hooks["SessionStart"].([]any), 2)

	// Remove: only gofer entries go; bd + permissions + user env stay.
	res, err = Install(AgentClaude, path, true, false)
	assert.NoErr(t, err)
	assert.Eq(t, 5, res.Removed)
	read()
	hooks = doc["hooks"].(map[string]any)
	assert.Len(t, hooks["SessionStart"].([]any), 1)
	_, hasStop := hooks["Stop"]
	assert.False(t, hasStop)
	assert.Eq(t, "9", doc["env"].(map[string]any)["CLAUDE_CODE_STOP_HOOK_BLOCK_CAP"])

	// Fresh codex file is created with env-less template.
	cpath := filepath.Join(dir, ".codex", "hooks.json")
	res, err = Install(AgentCodex, cpath, false, false)
	assert.NoErr(t, err)
	assert.True(t, res.Created)
	assert.Eq(t, 5, res.Added)
	b, _ := os.ReadFile(cpath)
	assert.True(t, strings.Contains(string(b), "gofer hook codex"))
	assert.False(t, strings.Contains(string(b), "env"))

	// Invalid JSON: refused without force, replaced with force.
	assert.NoErr(t, os.WriteFile(cpath, []byte("{oops"), 0o644))
	_, err = Install(AgentCodex, cpath, false, false)
	assert.Err(t, err)
	res, err = Install(AgentCodex, cpath, false, true)
	assert.NoErr(t, err)
	assert.Len(t, res.Notes, 1)

	// Mixed entry: foreign hook kept when the gofer hook in the same entry is removed.
	mixed := `{"hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"gofer hook claude"},{"type":"command","command":"notify-send done"}]}]}}`
	assert.NoErr(t, os.WriteFile(path, []byte(mixed), 0o644))
	_, err = Install(AgentClaude, path, true, false)
	assert.NoErr(t, err)
	read()
	stop = doc["hooks"].(map[string]any)["Stop"].([]any)
	assert.Len(t, stop, 1)
	assert.Len(t, stop[0].(map[string]any)["hooks"].([]any), 1)

	_, err = ConfigFileFor("gemini", dir)
	assert.Err(t, err)
	p, _ := ConfigFileFor(AgentCodex, dir)
	assert.Eq(t, cpath, p)
}
