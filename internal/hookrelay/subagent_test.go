package hookrelay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/client"
)

func TestSubagentEventsReportDeltaAndID(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1"}
	var log strings.Builder
	p := payload(t, "claude", map[string]any{
		"session_id": "s1", "hook_event_name": "SubagentStart", "agent_id": "ag-7", "agent_type": "Explore",
	})
	assert.Eq(t, "ag-7", p.AgentID)
	assert.Eq(t, "Explore", p.AgentType)
	res, err := Run(f, p, fastOpts(&log))
	assert.NoErr(t, err)
	assert.False(t, res.Blocked)
	// Stop without any agent fields (lenient parse) and a numeric id still work.
	_, err = Run(f, payload(t, "claude", map[string]any{"session_id": "s1", "hook_event_name": "SubagentStop", "agent_id": "ag-7"}), fastOpts(&log))
	assert.NoErr(t, err)
	_, err = Run(f, payload(t, "claude", map[string]any{"session_id": "s1", "hook_event_name": "SubagentStop", "agent_id": 42}), fastOpts(&log))
	assert.NoErr(t, err)
	assert.Len(t, f.beats, 3)
	assert.Eq(t, "SubagentStart", f.beats[0].Event)
	assert.Eq(t, 1, f.beats[0].SubagentDelta)
	assert.Eq(t, "ag-7", f.beats[0].SubagentID)
	assert.Eq(t, -1, f.beats[1].SubagentDelta)
	assert.Eq(t, "42", f.beats[2].SubagentID)
}

func TestStopWaitCappedByServerBudget(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{
		SessionID: "s1", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn, WaitBudgetSec: 2,
	}
	var log strings.Builder
	opts := fastOpts(&log)
	opts.Wait = 600 * time.Second
	_, err := Run(f, payload(t, "codex", map[string]any{"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "x"}), opts)
	assert.NoErr(t, err)
	assert.Eq(t, int64(2), f.turns["dec-1"].TimeoutSec)
	assert.True(t, strings.Contains(log.String(), "capped by server budget"))

	// A budget larger than --wait, or none (older server), changes nothing.
	f2 := newFake()
	f2.sessions["s1"] = client.AgentSession{SessionID: "s1", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn, WaitBudgetSec: 9999}
	opts.Wait = 3 * time.Second
	_, _ = Run(f2, payload(t, "codex", map[string]any{"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "x"}), opts)
	assert.Eq(t, int64(3), f2.turns["dec-1"].TimeoutSec)
}

func TestIgnoredCwd(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "u")
	none := func(string) string { return "" }
	mem := filepath.Join(home, ".codex", "memories")
	assert.True(t, IgnoredCwd(mem, home, none))
	assert.True(t, IgnoredCwd(filepath.Join(mem, "sub"), home, none))
	assert.False(t, IgnoredCwd(mem+"-x", home, none), "prefix must stop at a separator")
	assert.False(t, IgnoredCwd(filepath.Join(home, ".codex"), home, none))
	assert.False(t, IgnoredCwd("", home, none))

	extra := filepath.Join(string(filepath.Separator), "srv", "scratch")
	env := func(k string) string {
		if k == EnvIgnoreCwds {
			return extra + string(os.PathListSeparator) + "~/tmp-agent"
		}
		return ""
	}
	assert.True(t, IgnoredCwd(filepath.Join(extra, "a"), home, env))
	assert.True(t, IgnoredCwd(filepath.Join(home, "tmp-agent"), home, env))
	assert.False(t, IgnoredCwd(filepath.Join(string(filepath.Separator), "srv", "other"), home, env))
}

func TestIgnoredCwdWindowsFold(t *testing.T) {
	dirs := []string{`C:\Users\Me\.codex\memories`}
	assert.True(t, ignoredCwdIn(`c:/users/me/.codex/Memories/x`, dirs, true))
	assert.True(t, ignoredCwdIn(`C:\USERS\ME\.CODEX\MEMORIES`, dirs, true))
	assert.False(t, ignoredCwdIn(`C:\Users\Me\.codex\memories2`, dirs, true))
	assert.False(t, ignoredCwdIn(`c:/users/me/.codex/Memories`, dirs, false), "case-sensitive off Windows")
}

func TestClaudeTemplateInstallsSubagentHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	_, err := Install(AgentClaude, path, false, false)
	assert.NoErr(t, err)
	b, _ := os.ReadFile(path)
	for _, ev := range []string{`"SubagentStart"`, `"SubagentStop"`} {
		assert.True(t, strings.Contains(string(b), ev), ev)
	}
	assert.True(t, HasRelayHooksAt(AgentClaude, path))
	_, err = Install(AgentClaude, path, true, false)
	assert.NoErr(t, err)
	b, _ = os.ReadFile(path)
	assert.False(t, strings.Contains(string(b), "Subagent"))
}
