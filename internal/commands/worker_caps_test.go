package commands

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/wsproto"
)

// workerCfg builds the resolved worker config snapshot exactly as runWorker does,
// so the capability helpers are exercised on the same input production feeds them.
func workerCfg(t *testing.T, agents map[string]config.AgentConfig) *config.Config {
	t.Helper()
	t.Setenv(config.EnvConfigDir, t.TempDir()) // hermetic db-path resolution
	return workerConfigToConfig(&config.WorkerConfig{WorkerID: "w1", Agents: agents})
}

// briefFor returns the reported brief for key (zero value when absent).
func briefFor(briefs []wsproto.AgentBrief, key string) (wsproto.AgentBrief, bool) {
	for _, b := range briefs {
		if b.Key == key {
			return b, true
		}
	}
	return wsproto.AgentBrief{}, false
}

// TestAgentBriefsIncludesBuiltinExec: a worker with NO agents block at all (the
// canonical exec-only worker) must still advertise the built-in exec agent —
// agent.ResolveAgent makes it runnable regardless, and the hub now treats this
// report as authoritative, so under-reporting would get every exec job rejected.
func TestAgentBriefsIncludesBuiltinExec(t *testing.T) {
	cfg := workerCfg(t, nil)

	briefs := agentBriefs(cfg)
	got, ok := briefFor(briefs, agent.ExecAgentKey)
	if !ok {
		t.Fatalf("built-in exec missing from agent_caps: %+v", briefs)
	}
	if got.Type != agent.TypeExec {
		t.Fatalf("built-in exec Type = %q, want %q", got.Type, agent.TypeExec)
	}
	if got.Interactive {
		t.Fatal("built-in exec must not be reported interactive")
	}
	// The back-compat key list must agree — P3 validates on it.
	if keys := agentKeys(cfg); !reflect.DeepEqual(keys, []string{agent.ExecAgentKey}) {
		t.Fatalf("agents key list = %v, want [%s]", keys, agent.ExecAgentKey)
	}
}

// TestAgentBriefsBareExecBlockKeepsExecType: a declared but bare `exec:` block (no
// explicit `type:`) is normalised to exec by agent.ResolveAgent — the report must
// carry that true type, not the raw config's empty string.
func TestAgentBriefsBareExecBlockKeepsExecType(t *testing.T) {
	cfg := workerCfg(t, map[string]config.AgentConfig{
		agent.ExecAgentKey: {}, // bare `exec:` — no type
	})

	got, ok := briefFor(agentBriefs(cfg), agent.ExecAgentKey)
	if !ok {
		t.Fatal("exec missing from agent_caps")
	}
	if got.Type != agent.TypeExec {
		t.Fatalf("bare exec block reported Type = %q, want %q (the raw map would give \"\")",
			got.Type, agent.TypeExec)
	}
}

// TestAgentBriefsDeclaredCLIAgent: a declared cli-agent reports its real key, type
// and interactive flag (the fields the UI cascade exists for) — alongside the
// always-present built-in exec.
func TestAgentBriefsDeclaredCLIAgent(t *testing.T) {
	cfg := workerCfg(t, map[string]config.AgentConfig{
		"claude": {Type: agent.TypeCLIAgent, Command: "claude", Interactive: true},
		"codex":  {Type: agent.TypeCLIAgent, Command: "codex"},
	})

	briefs := agentBriefs(cfg)
	want := []wsproto.AgentBrief{
		{Key: "claude", Type: agent.TypeCLIAgent, Interactive: true},
		{Key: "codex", Type: agent.TypeCLIAgent, Interactive: false},
		{Key: agent.ExecAgentKey, Type: agent.TypeExec, Interactive: false},
	}
	if !reflect.DeepEqual(briefs, want) {
		t.Fatalf("agent_caps:\n got %+v\nwant %+v", briefs, want)
	}
}

// TestAgentKeysMatchAgentBriefs: the key list and the typed caps are built from the
// same resolved set, so they can never drift (P3 validates on the keys, the UI reads
// the caps — a mismatch would silently accept/reject the wrong agents).
func TestAgentKeysMatchAgentBriefs(t *testing.T) {
	cfg := workerCfg(t, map[string]config.AgentConfig{
		"claude": {Type: agent.TypeCLIAgent, Command: "claude", Interactive: true},
	})

	keys := agentKeys(cfg)
	briefs := agentBriefs(cfg)
	briefKeys := make([]string, 0, len(briefs))
	for _, b := range briefs {
		briefKeys = append(briefKeys, b.Key)
	}
	if !reflect.DeepEqual(keys, briefKeys) {
		t.Fatalf("key-set drift: agents=%v vs agent_caps=%v", keys, briefKeys)
	}
	if !reflect.DeepEqual(keys, []string{"claude", agent.ExecAgentKey}) {
		t.Fatalf("keys = %v, want [claude exec] (sorted, incl. built-in exec)", keys)
	}
}

// TestShippedWorkerExampleReportsExec is the concrete regression lock: the SHIPPED
// config/worker.example.yaml has its entire `agents:` block commented out ("纯 exec
// job 不需要本段") while its project runs exec jobs. That canonical worker must
// advertise exec in BOTH the typed caps and the back-compat key list.
func TestShippedWorkerExampleReportsExec(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	wc, err := loadWorkerConfig(filepath.Join("..", "..", "config", "worker.example.yaml"))
	if err != nil {
		t.Fatalf("load shipped worker.example.yaml: %v", err)
	}
	if len(wc.Agents) != 0 {
		t.Fatalf("fixture drift: worker.example.yaml now declares agents %v — this test "+
			"guards the commented-out (no agents) case", wc.Agents)
	}
	cfg := workerConfigToConfig(wc)
	t.Logf("shipped exec-only worker reports: agents=%v agent_caps=%+v",
		agentKeys(cfg), agentBriefs(cfg))

	if keys := agentKeys(cfg); !reflect.DeepEqual(keys, []string{agent.ExecAgentKey}) {
		t.Fatalf("shipped exec-only worker advertises agents %v, want [%s]",
			keys, agent.ExecAgentKey)
	}
	got, ok := briefFor(agentBriefs(cfg), agent.ExecAgentKey)
	if !ok {
		t.Fatal("shipped exec-only worker advertises NO exec in agent_caps")
	}
	if got.Type != agent.TypeExec {
		t.Fatalf("exec reported Type = %q, want %q", got.Type, agent.TypeExec)
	}
}
