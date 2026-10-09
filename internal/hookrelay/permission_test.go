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

// fake hub side of the permission endpoints (state lives on fakeAPI.turns).
var permMu sync.Mutex
var permOpened = map[*fakeAPI][]client.SessionPermission{}
var permResolved = map[*fakeAPI][]string{}

func (f *fakeAPI) OpenSessionPermission(sid string, p client.SessionPermission) (client.Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a := f.sessions[sid]; a.WaitReason == "" && a.PermissionWaitReason == "" {
		return client.Decision{}, &client.StatusError{Status: 409, Msg: "relay off"}
	}
	permMu.Lock()
	permOpened[f] = append(permOpened[f], p)
	permMu.Unlock()
	d := client.Decision{ID: "perm-1", Question: "需要授权：" + p.Summary, State: "OPEN", TimeoutSec: p.TimeoutSec, SessionID: sid, Kind: "permission"}
	f.turns[d.ID] = d
	return d, nil
}

func (f *fakeAPI) ResolveSessionPermission(sid, fp string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	permMu.Lock()
	permResolved[f] = append(permResolved[f], fp)
	permMu.Unlock()
	n := 0
	for id, d := range f.turns {
		if d.Kind == "permission" && d.State == "OPEN" {
			d.State, d.ReleasedBy = "EXPIRED", "terminal"
			f.turns[id] = d
			n++
		}
	}
	return n, nil
}

// tickingOpts advances a fake clock one second per read, so a wait budget runs out
// after a handful of polls instead of in real time.
func tickingOpts(t *testing.T, log *strings.Builder, wait time.Duration) Options {
	cur := time.Unix(1000, 0)
	var mu sync.Mutex
	return Options{Wait: wait, PollSec: 1, Log: log, sleep: func(time.Duration) {},
		PermissionStateDir: filepath.Join(t.TempDir(), "permission-pending"),
		now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			cur = cur.Add(time.Second)
			return cur
		}}
}

func permPayload(t *testing.T, cmd string) Payload {
	return payload(t, "claude", map[string]any{
		"session_id": "s1", "hook_event_name": "PermissionRequest", "cwd": "/w",
		"tool_name": "Bash", "tool_input": map[string]any{"command": cmd, "description": "x"},
		"permission_suggestions": []any{
			map[string]any{"type": "addRules", "rules": []any{map[string]any{"toolName": "Bash", "ruleContent": "npm test"}},
				"behavior": "allow", "destination": "localSettings"},
			map[string]any{"type": "setMode", "mode": "acceptEdits", "destination": "session"},
		},
	})
}

func TestPermissionViewSummaryRedactionTruncation(t *testing.T) {
	sum, detail := PermissionView("Bash", json.RawMessage(`{"command":"curl -H 'Authorization: Bearer abcdefghijklmnop' --token s3cr3t https://x","description":"d"}`))
	assert.True(t, strings.HasPrefix(sum, "Bash `curl"), sum)
	for _, leak := range []string{"abcdefghijklmnop", "s3cr3t"} {
		assert.False(t, strings.Contains(sum, leak), "summary leaks "+leak+": "+sum)
		assert.False(t, strings.Contains(detail, leak), "detail leaks "+leak)
	}
	_, detail = PermissionView("Bash", json.RawMessage(`{"command":"make a && make b"}`))
	assert.True(t, strings.Contains(detail, "make a && make b"), detail)
	_, detail = PermissionView("mcp__x__call", json.RawMessage(`{"api_key":"zzz-secret-value","nested":{"password":"p4ss"},"q":"hello"}`))
	assert.False(t, strings.Contains(detail, "zzz-secret-value"))
	assert.False(t, strings.Contains(detail, "p4ss"))
	assert.True(t, strings.Contains(detail, "hello"))

	big := strings.Repeat("x", 10000)
	sum, detail = PermissionView("Write", json.RawMessage(`{"file_path":"/w/a.txt","content":"`+big+`"}`))
	assert.Eq(t, "Write /w/a.txt", sum)
	assert.True(t, len(detail) < permissionInputBytes+64, len(detail))
	assert.True(t, strings.HasSuffix(detail, "（已截断）"))

	long, _ := PermissionView("Bash", json.RawMessage(`{"command":"`+strings.Repeat("y", 500)+`"}`))
	assert.True(t, len([]rune(long)) <= permissionSummaryRunes+1)
}

func TestPermissionFingerprintIgnoresKeyOrder(t *testing.T) {
	a := PermissionFingerprint("Bash", json.RawMessage(`{"command":"ls","description":"d"}`))
	b := PermissionFingerprint("Bash", json.RawMessage(`{ "description":"d", "command":"ls" }`))
	c := PermissionFingerprint("Bash", json.RawMessage(`{"command":"ls -l","description":"d"}`))
	assert.Eq(t, a, b)
	assert.NotEq(t, a, c)
}

func TestSuggestionLabels(t *testing.T) {
	assert.Eq(t, "规则 Bash(npm --version) · 本项目（本地）", SuggestionLabel(json.RawMessage(
		`{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"npm --version"}],"behavior":"allow","destination":"localSettings"}`)))
	assert.Eq(t, "目录 /w/proj · 本会话", SuggestionLabel(json.RawMessage(
		`{"type":"addDirectories","directories":["/w/proj"],"destination":"session"}`)))
	assert.Eq(t, "切换到 acceptEdits 模式 · 本会话", SuggestionLabel(json.RawMessage(
		`{"type":"setMode","mode":"acceptEdits","destination":"session"}`)))
}

func TestPermissionDecisionJSONShape(t *testing.T) {
	sug := json.RawMessage(`{"type":"setMode","mode":"acceptEdits","destination":"session"}`)
	cases := []struct{ answer, want string }{
		{"allow", `{"hookSpecificOutput":{"decision":{"behavior":"allow"},"hookEventName":"PermissionRequest"}}`},
		{"always:0", `{"hookSpecificOutput":{"decision":{"behavior":"allow","updatedPermissions":[{"type":"setMode","mode":"acceptEdits","destination":"session"}]},"hookEventName":"PermissionRequest"}}`},
		{"deny", `{"hookSpecificOutput":{"decision":{"behavior":"deny","message":"用户在 gofer web 上拒绝了这次操作"},"hookEventName":"PermissionRequest"}}`},
		{"deny:不要删", `{"hookSpecificOutput":{"decision":{"behavior":"deny","message":"用户在 gofer web 上拒绝了这次操作：不要删"},"hookEventName":"PermissionRequest"}}`},
	}
	for _, tc := range cases {
		d, ok := decisionFromAnswer(tc.answer, []json.RawMessage{sug})
		assert.True(t, ok, tc.answer)
		assert.Eq(t, tc.want, string(PermissionJSON(d)), tc.answer)
	}
	for _, bad := range []string{"always:1", "always:x", "maybe", ""} {
		_, ok := decisionFromAnswer(bad, []json.RawMessage{sug})
		assert.False(t, ok, bad)
	}
}

func TestPermissionRequestReportsAndDoesNotWaitWhenPersonIsAtKeyboard(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", Agent: "claude", RelayMode: client.RelayModeAuto}
	var log strings.Builder
	res, err := Run(f, permPayload(t, "rm -rf node_modules"), tickingOpts(t, &log, time.Minute))
	assert.NoErr(t, err)
	assert.Nil(t, res.Permission)
	assert.Eq(t, 1, len(f.beats))
	assert.Eq(t, "PermissionRequest", f.beats[0].Event)
	assert.Eq(t, "需要授权：Bash `rm -rf node_modules`", f.beats[0].LastMessage)
	assert.Eq(t, 0, len(permOpened[f]))
	assert.Eq(t, 0, f.waits)
}

func TestPermissionRequestWaitsAndReturnsWebAnswer(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", Agent: "claude", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	f.answerAfter, f.answer = 2, "always:0"
	var log strings.Builder
	opts := tickingOpts(t, &log, time.Minute)
	p := permPayload(t, "npm test")
	res, err := Run(f, p, opts)
	assert.NoErr(t, err)
	assert.NotNil(t, res.Permission)
	assert.Eq(t, "allow", res.Permission.Behavior)
	assert.Eq(t, 1, len(res.Permission.UpdatedPermissions))
	assert.True(t, strings.Contains(string(res.Permission.UpdatedPermissions[0]), `"ruleContent":"npm test"`))
	opened := permOpened[f]
	assert.Eq(t, 1, len(opened))
	assert.Eq(t, "Bash `npm test`", opened[0].Summary)
	assert.Eq(t, 2, len(opened[0].Suggestions))
	assert.Eq(t, PermissionFingerprint("Bash", p.ToolInput), opened[0].Fingerprint)
	// the marker is gone once the hook returned
	assert.False(t, PermissionPending(p, opts.PermissionStateDir))
}

func TestPermissionRequestTimeoutPrintsNothing(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", Agent: "claude", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	var log strings.Builder
	res, err := Run(f, permPayload(t, "ls"), tickingOpts(t, &log, 5*time.Second))
	assert.NoErr(t, err)
	assert.Nil(t, res.Permission)
	assert.True(t, f.waits > 0)
	assert.True(t, strings.Contains(log.String(), "budget exhausted"), log.String())
}

func TestPermissionRequestReleasedLeavesTerminalDialog(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", Agent: "claude", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	f.relayOffAfter = 1
	var log strings.Builder
	res, err := Run(f, permPayload(t, "ls"), tickingOpts(t, &log, time.Minute))
	assert.NoErr(t, err)
	assert.Nil(t, res.Permission)
}

func TestPostToolUseSettlesOnlyItsOwnPendingPrompt(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", Agent: "claude"}
	var log strings.Builder
	opts := tickingOpts(t, &log, time.Minute)
	pending := payload(t, "claude", map[string]any{"session_id": "s1", "hook_event_name": "PostToolUse",
		"tool_name": "Edit", "tool_input": map[string]any{"file_path": "/w/a", "old_string": "a", "new_string": "b"}})
	other := payload(t, "claude", map[string]any{"session_id": "s1", "hook_event_name": "PostToolUse",
		"tool_name": "Edit", "tool_input": map[string]any{"file_path": "/w/b", "old_string": "a", "new_string": "b"}})
	marker := permissionMarker(opts.PermissionStateDir, "s1", PermissionFingerprint(pending.ToolName, pending.ToolInput))
	assert.NoErr(t, os.MkdirAll(filepath.Dir(marker), 0o700))
	assert.NoErr(t, os.WriteFile(marker, []byte("perm-1\n"), 0o600))

	assert.True(t, SkipPostToolUse(other, opts.PermissionStateDir))
	assert.False(t, SkipPostToolUse(pending, opts.PermissionStateDir))
	bash := payload(t, "claude", map[string]any{"session_id": "s1", "hook_event_name": "PostToolUse", "tool_name": "Bash"})
	assert.False(t, SkipPostToolUse(bash, opts.PermissionStateDir))

	_, err := Run(f, other, opts)
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(permResolved[f]))
	_, err = Run(f, pending, opts)
	assert.NoErr(t, err)
	assert.Eq(t, []string{PermissionFingerprint(pending.ToolName, pending.ToolInput)}, permResolved[f])
	_, statErr := os.Stat(marker)
	assert.True(t, os.IsNotExist(statErr))
}

// TestPermissionRequestUsesPermissionVerdict: a session whose Stop verdict is held
// back by the supervising gate still mirrors (and waits on) its permission prompt.
func TestPermissionRequestUsesPermissionVerdict(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", Agent: "claude", RelayMode: client.RelayModeAuto,
		WaitReasonDetail: "supervising 1 subagents", PermissionWaitReason: client.WaitIdleProbe}
	f.answerAfter, f.answer = 1, "allow"
	f.released = false
	var log strings.Builder
	res, err := Run(f, permPayload(t, "ls"), tickingOpts(t, &log, time.Minute))
	assert.NoErr(t, err)
	assert.NotNil(t, res.Permission)
	assert.Eq(t, "allow", res.Permission.Behavior)
	assert.Eq(t, 1, len(permOpened[f]))
}

// TestPermissionRequestEarlyExitsReleaseTheCard: every exit that leaves the prompt to
// the terminal while the web card may still be OPEN closes it; exits where the card
// is already settled server-side (expired / relay_off / answered) do not.
func TestPermissionRequestEarlyExitsReleaseTheCard(t *testing.T) {
	cases := []struct {
		name    string
		script  func(f *fakeAPI)
		wait    time.Duration
		release bool
	}{
		{"transient failures", func(f *fakeAPI) { f.failWaitTimes = 100 }, time.Hour, true},
		{"404", func(f *fakeAPI) { f.failWaitTimes, f.failWaitCode = 1, 404 }, time.Hour, true},
		{"unusable answer", func(f *fakeAPI) { f.answerAfter, f.answer = 1, "always:9" }, time.Minute, true},
		{"budget exhausted", func(f *fakeAPI) {}, 5 * time.Second, true},
		{"relay off", func(f *fakeAPI) { f.relayOffAfter = 1 }, time.Minute, false},
		{"answered", func(f *fakeAPI) { f.answerAfter, f.answer = 1, "deny" }, time.Minute, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.sessions["s1"] = client.AgentSession{SessionID: "s1", Agent: "claude", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
			tc.script(f)
			var log strings.Builder
			p := permPayload(t, "ls")
			res, err := Run(f, p, tickingOpts(t, &log, tc.wait))
			assert.NoErr(t, err)
			if tc.name != "answered" {
				assert.Nil(t, res.Permission)
			}
			permMu.Lock()
			got := permResolved[f]
			permMu.Unlock()
			if tc.release {
				assert.Eq(t, []string{PermissionFingerprint(p.ToolName, p.ToolInput)}, got, log.String())
			} else {
				assert.Eq(t, 0, len(got), log.String())
			}
		})
	}
}
