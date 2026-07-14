package agent

import (
	"flag"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// hangHelperArg re-executes the test binary as a process that just hangs, giving the
// probe tests a version command that is guaranteed present on PATH (it is this very
// binary) and guaranteed to outlive every budget — on any OS, with no dependency on
// what is installed on the host.
const hangHelperArg = "gofer-detect-hang-helper"

// TestHelperHangProcess is not a test. It is the child process spawned by the probe
// tests below; it only hangs when re-executed with hangHelperArg, and skips during a
// normal `go test` run.
func TestHelperHangProcess(t *testing.T) {
	if !slices.Contains(flag.Args(), hangHelperArg) {
		t.Skip("helper process; only runs when a probe test re-executes this binary")
	}
	time.Sleep(60 * time.Second)
}

// hangingAgent returns a cli-agent whose command resolves on PATH (it IS the test
// binary) and whose version probe hangs far past any budget.
func hangingAgent(t *testing.T) config.AgentConfig {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate the test binary: %v", err)
	}
	return config.AgentConfig{
		Type:    TypeCLIAgent,
		Command: self,
		Detect: config.DetectConfig{
			Command: self,
			Args:    []string{"-test.run=TestHelperHangProcess", hangHelperArg},
		},
	}
}

// TestDetectBudgetWithEveryProbeHanging is the budget's hard test: EVERY version probe
// hangs, yet the whole batch must still come back inside detectBudget. Detect runs on
// the reload path, which owes an HTTP answer within 10s — the old serial 5s-per-agent
// walk blew that outright.
func TestDetectBudgetWithEveryProbeHanging(t *testing.T) {
	agents := map[string]config.AgentConfig{}
	for _, k := range []string{"a1", "a2", "a3", "a4", "a5", "a6"} {
		agents[k] = hangingAgent(t)
	}

	start := time.Now()
	got := DefaultDetector().Detect(agents)
	elapsed := time.Since(start)
	t.Logf("6 hanging probes returned in %s (budget %s, per-probe %s)", elapsed, detectBudget, detectTimeout)

	if elapsed > detectBudget+300*time.Millisecond {
		t.Fatalf("detect took %s, must stay within the %s budget", elapsed, detectBudget)
	}
	if elapsed < 500*time.Millisecond {
		t.Fatalf("detect returned in %s — the probes did not actually run, the test proves nothing", elapsed)
	}
	for key, res := range got {
		if !res.Available {
			t.Errorf("%s: a hung version probe must NOT make an agent unavailable: %+v", key, res)
		}
	}
}

// TestHungVersionProbeKeepsAvailable is THE false-negative guard: the CLI is on PATH but
// its `--version` hangs (slow start / first-run wizard / auth prompt — routine for node
// CLIs). Availability must survive it; only the version string is lost. A false negative
// would drop the agent from the caps report and get its jobs rejected.
func TestHungVersionProbeKeepsAvailable(t *testing.T) {
	got := DefaultDetector().Detect(map[string]config.AgentConfig{"slow": hangingAgent(t)})

	res := got["slow"]
	if !res.Available {
		t.Fatalf("hung version probe flipped Available to false: %+v", res)
	}
	if res.Version != "" {
		t.Errorf("version = %q, want empty (the probe never answered)", res.Version)
	}
}

// TestFailingVersionProbeKeepsAvailable: same guard for the non-zero-exit / missing-probe
// case — the version command is broken, the agent's own command is fine.
func TestFailingVersionProbeKeepsAvailable(t *testing.T) {
	ac := config.AgentConfig{
		Type:    TypeCLIAgent,
		Command: "go", // on PATH: the tests are run by it
		Detect:  config.DetectConfig{Command: "__no_such_probe_xyz__", Args: []string{"--version"}},
	}

	got := DefaultDetector().Detect(map[string]config.AgentConfig{"broken-probe": ac})
	if res := got["broken-probe"]; !res.Available || res.Version != "" {
		t.Fatalf("broken version probe must leave Available=true + empty version, got %+v", res)
	}

	// The display path (gofer agent detect / GET /v1/agents / MCP ListAgents) must agree.
	reg := NewRegistry(&config.Config{Agents: map[string]config.AgentConfig{"broken-probe": ac}})
	if res := reg.Detect("broken-probe"); !res.Available {
		t.Fatalf("Registry.Detect disagrees with the batch detector: %+v", res)
	}
}

// TestExecIsExemptFromAFailingDetect: exec needs no external CLI, so its availability must
// be decided BEFORE any detect block is consulted. The shipped configs probe exec with
// `sh -c true`; on a host without `sh` (Windows) honouring that block would report the
// BUILT-IN exec agent unavailable and reject every exec job on the box.
func TestExecIsExemptFromAFailingDetect(t *testing.T) {
	ac := config.AgentConfig{
		Type:   TypeExec,
		Detect: config.DetectConfig{Command: "__no_such_cmd__"},
	}

	if res := (DefaultDetector().Detect(map[string]config.AgentConfig{"exec": ac}))["exec"]; !res.Available {
		t.Fatalf("batch detector: exec with a failing detect must stay available, got %+v", res)
	}
	reg := NewRegistry(&config.Config{Agents: map[string]config.AgentConfig{"exec": ac}})
	if res := reg.Detect("exec"); !res.Available {
		t.Fatalf("Registry.Detect: exec with a failing detect must stay available, got %+v", res)
	}
}

// TestNoDetectBlockInstalledCLIIsAvailable pins the fix for the live false negative: an
// agent whose CLI is installed but whose config carries no detect stanza used to be
// reported unavailable ("no detect command configured") — that verdict said nothing about
// the host and everything about the config. Availability is a PATH lookup now.
func TestNoDetectBlockInstalledCLIIsAvailable(t *testing.T) {
	ac := config.AgentConfig{Type: TypeCLIAgent, Command: "go"} // no Detect block

	if res := (DefaultDetector().Detect(map[string]config.AgentConfig{"goagent": ac}))["goagent"]; !res.Available {
		t.Fatalf("batch detector: installed CLI without a detect block must be available, got %+v", res)
	}
	reg := NewRegistry(&config.Config{Agents: map[string]config.AgentConfig{"goagent": ac}})
	if res := reg.Detect("goagent"); !res.Available {
		t.Fatalf("Registry.Detect: installed CLI without a detect block must be available, got %+v", res)
	}
}

// TestDetectUninstalledCLI: the one thing that DOES make an agent unavailable — its
// command does not resolve on PATH.
func TestDetectUninstalledCLI(t *testing.T) {
	ac := config.AgentConfig{Type: TypeCLIAgent, Command: "__no_such_cli_xyz__"}

	res := (DefaultDetector().Detect(map[string]config.AgentConfig{"ghost": ac}))["ghost"]
	if res.Available {
		t.Fatalf("a command that is not on PATH must be unavailable: %+v", res)
	}
	if res.Error == "" {
		t.Error("an unavailable agent must carry the reason")
	}
}
