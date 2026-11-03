package job

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// Fixtures for the two ends of a child receipt (design 2026-11-03-resident-job-child-wakeup
// T7 父 job 结束 / T8 子 job 接管与重试).
//
// The jobs they submit are GATED: the agent blocks until the test creates a file, so the
// test can register receipts on a job that is certainly still running and only then decide
// how that job ends — no fixed sleeps, no racing a process that already exited.

// childWakeTransient is a provider error the built-in "codex" patterns recognise (see
// auto_resume_test.go), so the same agent name takes the automatic-continuation path.
const childWakeTransient = "ERROR: Selected model is at capacity. Please try a different model."

// childWakeGate is the file a gated agent waits for.
type childWakeGate struct{ path string }

// newChildWakeGate returns a gate under the test's own TempDir.
func newChildWakeGate(t *testing.T) childWakeGate {
	t.Helper()
	return childWakeGate{path: filepath.Join(t.TempDir(), "gate")}
}

// Release lets every gated job finish.
func (g childWakeGate) Release(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(g.path, []byte("go"), 0o644); err != nil {
		t.Fatalf("release the gate: %v", err)
	}
}

// childWakeAgents is the agent set the fixtures run on: "gated" succeeds, "plain" fails
// without a provider error, "codex" fails with one (so it is taken over / retried).
func childWakeAgents(bin string, gate childWakeGate) map[string]config.AgentConfig {
	return map[string]config.AgentConfig{
		"gated": {Type: agent.TypeCLIAgent, Command: bin,
			Args: []string{"wait-file-exit", gate.path, "30s", "0"}},
		"plain": {Type: agent.TypeCLIAgent, Command: bin,
			Args: []string{"wait-file-exit", gate.path, "30s", "1", "boom"}},
		"codex": {Type: agent.TypeCLIAgent, Command: bin,
			Args:          []string{"wait-file-exit", gate.path, "30s", "1", childWakeTransient},
			SessionResume: []string{"printf", "resumed {{prompt}}"}},
	}
}

// childWakeProjectCfg is the project half of the fixtures: the local runner plus the
// remote-w1 pin the resident-parent fixture needs.
func childWakeProjectCfg(root string) (map[string]config.ProjectConfig, map[string]config.RunnerConfig) {
	projects := map[string]config.ProjectConfig{
		"self": {
			HostPath:         root,
			AllowedAgents:    []string{"gated", "plain", "codex", "exec"},
			AllowedRunners:   []string{"local", "remote-w1"},
			AllowExec:        true,
			AllowInteractive: boolPtr(true),
		},
	}
	runners := map[string]config.RunnerConfig{"remote-w1": {Type: "worker", WorkerID: "w1"}}
	return projects, runners
}

// newChildWakeService wires a Service over cfg (the test-side half of newServiceFromCfg)
// with a recording worker selector, so both a real local job and a resident remote session
// can be driven in one test.
func newChildWakeService(t *testing.T, cfg *config.Config, root string) (*Service, *recordingSessionSender) {
	t.Helper()
	if cfg.Server.Language == "" {
		cfg.Server.Language = string(i18n.ZhCN)
	}
	sender := &recordingSessionSender{online: true}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	meta, err := jobstore.Open(jobstoreDBPath(root))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	return drainOnClose(t, NewService(cfg, projReg, agentReg, runners, meta, sender)), sender
}
