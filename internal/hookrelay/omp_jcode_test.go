package hookrelay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/client"
)

func TestParsePayloadAcceptsOmpAndJcodeNames(t *testing.T) {
	assert.True(t, ValidAgent(AgentOmp))
	assert.True(t, ValidAgent(AgentJcode))
	p, err := ParsePayload("omp", strings.NewReader(`{"session_id":"s1","cwd":"/w","hook_event_name":"Stop","last_assistant_message":"done","transcript_path":"/t.jsonl"}`))
	assert.NoErr(t, err)
	assert.Eq(t, AgentOmp, p.Agent)
	assert.Eq(t, "done", p.LastAssistantMessage)
}

func TestParseJcodePayloadFromEnv(t *testing.T) {
	env := map[string]string{
		"JCODE_HOOK_PAYLOAD": `{"cwd":"D:\\w","event":"post_tool","session_id":"session_fish_1","status":"ok","tool_name":"bash"}`,
	}
	p, err := ParseJcodePayload(func(k string) string { return env[k] }, nil)
	assert.NoErr(t, err)
	assert.Eq(t, AgentJcode, p.Agent)
	assert.Eq(t, "PostToolUse", p.Event)
	assert.Eq(t, "session_fish_1", p.SessionID)
	assert.Eq(t, `D:\w`, p.Cwd)
	assert.Eq(t, "bash", p.ToolName)

	// env-only fallback + turn_end carries the last assistant text.
	env = map[string]string{
		"JCODE_HOOK_EVENT": "turn_end", "JCODE_HOOK_SESSION_ID": "s2", "JCODE_HOOK_CWD": "/w",
		"JCODE_HOOK_LAST_ASSISTANT_TEXT": "all done",
	}
	p, err = ParseJcodePayload(func(k string) string { return env[k] }, nil)
	assert.NoErr(t, err)
	assert.Eq(t, "Stop", p.Event)
	assert.Eq(t, "all done", p.LastAssistantMessage)

	// an event gofer does not know maps to "" (Run ignores it); no session id is an error.
	env = map[string]string{"JCODE_HOOK_EVENT": "pre_tool", "JCODE_HOOK_SESSION_ID": "s3"}
	p, err = ParseJcodePayload(func(k string) string { return env[k] }, nil)
	assert.NoErr(t, err)
	assert.Eq(t, "", p.Event)
	_, err = ParseJcodePayload(func(string) string { return "" }, strings.NewReader(""))
	assert.Err(t, err)
}

// jcode hooks are detached: a Stop can never wait for (or inject) a reply, so
// even with relay switched on the observe-only runner just reports and returns.
func TestRunStopObserveOnlyNeverWaits(t *testing.T) {
	api := newFake()
	_, _ = api.RegisterSession(client.SessionRegister{SessionID: "j1", Agent: AgentJcode})
	_, _ = api.SetSessionRelayMode("j1", client.RelayModeOn)
	res, err := Run(api, Payload{Agent: AgentJcode, Event: "Stop", SessionID: "j1", LastAssistantMessage: "hello"}, Options{})
	assert.NoErr(t, err)
	assert.False(t, res.Blocked)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Eq(t, 0, api.waits)
	assert.Eq(t, 0, len(api.turns))
	assert.Eq(t, "hello", api.beats[len(api.beats)-1].LastMessage)
}

func TestInstallOmpExtensionLifecycle(t *testing.T) {
	root := t.TempDir()
	path, err := ConfigFileFor(AgentOmp, root)
	assert.NoErr(t, err)
	assert.Eq(t, filepath.Join(root, ".omp", "extensions", "gofer-relay.ts"), path)
	gpath, err := GlobalConfigFileFor(AgentOmp, root)
	assert.NoErr(t, err)
	assert.Eq(t, filepath.Join(root, ".omp", "agent", "extensions", "gofer-relay.ts"), gpath)

	res, err := Install(AgentOmp, path, false, false)
	assert.NoErr(t, err)
	assert.True(t, res.Created)
	b, _ := os.ReadFile(path)
	assert.StrContains(t, string(b), OmpExtensionMarker)
	assert.StrContains(t, string(b), "session_stop")
	assert.True(t, HasRelayHooks(AgentOmp, root))

	// idempotent re-install replaces our own file
	res, err = Install(AgentOmp, path, false, false)
	assert.NoErr(t, err)
	assert.Eq(t, 1, res.Replaced)

	// remove deletes the file
	res, err = Install(AgentOmp, path, true, false)
	assert.NoErr(t, err)
	assert.Eq(t, 1, res.Removed)
	_, serr := os.Stat(path)
	assert.True(t, os.IsNotExist(serr))
	assert.False(t, HasRelayHooks(AgentOmp, root))

	// a foreign file of the same name is never touched without --force
	assert.NoErr(t, os.MkdirAll(filepath.Dir(path), 0o755))
	assert.NoErr(t, os.WriteFile(path, []byte("// mine\n"), 0o644))
	_, err = Install(AgentOmp, path, false, false)
	assert.Err(t, err)
	_, err = Install(AgentOmp, path, true, false)
	assert.NoErr(t, err)
	b, _ = os.ReadFile(path)
	assert.Eq(t, "// mine\n", string(b))
	_, err = Install(AgentOmp, path, false, true)
	assert.NoErr(t, err)
	b, _ = os.ReadFile(path)
	assert.StrContains(t, string(b), OmpExtensionMarker)
}

func TestInstallJcodeMergesHooksTable(t *testing.T) {
	dir := t.TempDir()
	path, err := ConfigFileFor(AgentJcode, dir)
	assert.NoErr(t, err)
	assert.Eq(t, filepath.Join(dir, "config.toml"), path)

	orig := "[display]\ntheme = \"dark\"\n\n[hooks]\npre_tool_timeout_ms = 5000\nturn_end = \"~/bin/mine\"\nsession_end = \"\"\n\n[ambient]\nenabled = false\n"
	assert.NoErr(t, os.WriteFile(path, []byte(orig), 0o644))
	res, err := Install(AgentJcode, path, false, false)
	assert.NoErr(t, err)
	b, _ := os.ReadFile(path)
	got := string(b)
	assert.StrContains(t, got, "[display]\ntheme = \"dark\"")
	assert.StrContains(t, got, "pre_tool_timeout_ms = 5000")
	assert.StrContains(t, got, "[ambient]\nenabled = false")
	assert.StrContains(t, got, `session_start = "gofer hook jcode"`)
	assert.StrContains(t, got, `turn_start = "gofer hook jcode"`)
	assert.StrContains(t, got, `post_tool = "gofer hook jcode"`)
	assert.StrContains(t, got, `session_end = "gofer hook jcode"`) // an empty value counts as unset
	assert.NotContains(t, got, `session_end = ""`)
	// a user-owned command for the same event is kept, and reported
	assert.StrContains(t, got, `turn_end = "~/bin/mine"`)
	assert.NotContains(t, got, `turn_end = "gofer hook jcode"`)
	assert.True(t, len(res.Notes) > 0)
	assert.True(t, HasRelayHooks(AgentJcode, dir))

	// idempotent: second run changes nothing
	before := got
	_, err = Install(AgentJcode, path, false, false)
	assert.NoErr(t, err)
	b, _ = os.ReadFile(path)
	assert.Eq(t, before, string(b))

	// remove takes only our commands out
	res, err = Install(AgentJcode, path, true, false)
	assert.NoErr(t, err)
	assert.Eq(t, 4, res.Removed)
	b, _ = os.ReadFile(path)
	got = string(b)
	assert.NotContains(t, got, "gofer hook jcode")
	assert.StrContains(t, got, `turn_end = "~/bin/mine"`)
	assert.StrContains(t, got, "pre_tool_timeout_ms = 5000")
	assert.False(t, HasRelayHooks(AgentJcode, dir))
}

func TestInstallJcodeCreatesFileAndTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	res, err := Install(AgentJcode, path, false, false)
	assert.NoErr(t, err)
	assert.True(t, res.Created)
	b, _ := os.ReadFile(path)
	assert.StrContains(t, string(b), "[hooks]\n")
	// a config with no [hooks] table gets one appended, rest untouched
	assert.NoErr(t, os.WriteFile(path, []byte("[x]\na = 1\n"), 0o644))
	_, err = Install(AgentJcode, path, false, false)
	assert.NoErr(t, err)
	b, _ = os.ReadFile(path)
	assert.True(t, strings.HasPrefix(string(b), "[x]\na = 1\n"))
	assert.StrContains(t, string(b), "[hooks]\n")
}
