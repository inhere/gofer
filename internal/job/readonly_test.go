package job

import (
	"errors"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// newReadOnlyService wires a Service with the agent shapes the read-only gate
// distinguishes: `exec` (never read-only), `codex` (a cli-agent whose command is the
// argv-echoing test binary and whose read-only args come from the BUILT-IN codex
// entry, so the assertions cover the built-in table end to end), and `plain` (a
// cli-agent with no read-only mode at all).
func newReadOnlyService(t *testing.T, root string) *Service {
	t.Helper()
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"codex", "plain", "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			// Keyed "codex" on purpose: the built-in read-only args (`-s read-only`) apply,
			// while the argv belongs to the test binary, which echoes what it received.
			"codex": {
				Type:          agent.TypeCLIAgent,
				Command:       bin,
				Args:          []string{"argv", "{{prompt}}"},
				SessionResume: []string{"argv", "{{session_id}}", "{{prompt}}"},
			},
			// A user agent with no read-only mode: submitting --read-only must be refused.
			"plain": {Type: agent.TypeCLIAgent, Command: bin, Args: []string{"argv", "{{prompt}}"}},
		},
	}
	return newServiceFromCfg(t, root, cfg)
}

// TestSubmitReadOnlyRejectsExec: an exec job cannot run read-only at all (its argv is
// whatever the caller wrote), so the flag is refused at admission instead of silently
// running a writable command the caller believes is sandboxed.
func TestSubmitReadOnlyRejectsExec(t *testing.T) {
	root := t.TempDir()
	s := newReadOnlyService(t, root)

	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30, ReadOnly: true,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("exec read-only submit err = %v, want ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("exec read-only submit err = %q, want it to say why", err.Error())
	}
}

// TestSubmitReadOnlyRejectsAgentWithoutMode: a cli-agent with no read_only_args has no
// way to sandbox itself, so --read-only is refused with a pointer to the config key
// rather than running the job writable.
func TestSubmitReadOnlyRejectsAgentWithoutMode(t *testing.T) {
	root := t.TempDir()
	s := newReadOnlyService(t, root)

	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "plain", Runner: "local",
		Prompt: "audit this", Cwd: ".", TimeoutSec: 30, ReadOnly: true,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("cli read-only submit err = %v, want ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "read_only_args") {
		t.Fatalf("cli read-only submit err = %q, want it to name read_only_args", err.Error())
	}

	// The agent is otherwise perfectly runnable: the refusal is about read-only.
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "plain", Runner: "local",
		Prompt: "audit this", Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("plain agent without --read-only = %s (err=%s), want done", final.Status, final.Error)
	}
}

// TestSubmitReadOnlyRejectsACPWithoutMode: an acp-agent has no sandbox argv — its
// read-only mode is the protocol's (session/set_mode), mapped by the operator in
// acp.modes.read_only. Without that mapping the job is refused, naming the key.
func TestSubmitReadOnlyRejectsACPWithoutMode(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{})

	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local",
		Prompt: acpTestPrompt, Cwd: ".", TimeoutSec: 30, ReadOnly: true,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("acp read-only submit err = %v, want ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "acp.modes.read_only") {
		t.Fatalf("acp read-only submit err = %q, want it to name acp.modes.read_only", err.Error())
	}
}

// TestSubmitReadOnlyRendersSandboxArgs: a cli-agent read-only job carries the built-in
// sandbox args on the argv the CHILD actually received (the test binary echoes its own
// argv), appended at the end like agent_args, and the job row records the flag.
func TestSubmitReadOnlyRendersSandboxArgs(t *testing.T) {
	root := t.TempDir()
	s := newReadOnlyService(t, root)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "review the diff", Cwd: ".", TimeoutSec: 30, ReadOnly: true,
	})
	if final.Status != StatusDone {
		t.Fatalf("read-only status = %s (err=%s), want done", final.Status, final.Error)
	}
	if !final.ReadOnly {
		t.Fatalf("job result must record read_only: %+v", final)
	}

	got := readJobLog(t, final, store.StdoutFile)
	if !strings.Contains(got, "\n-s\n") || !strings.Contains(got, "\nread-only\n") {
		t.Fatalf("child argv lacks the built-in read-only args: %q", got)
	}
	if last := strings.TrimSpace(got); !strings.HasSuffix(last, "read-only") {
		t.Fatalf("read-only args must be appended at the END of argv: %q", got)
	}
}

// TestACPReadOnlySetsMode: an acp-agent read-only job is switched with session/set_mode
// BEFORE the prompt turn, using the operator's acp.modes.read_only id, and the
// structured stream records it.
func TestACPReadOnlySetsMode(t *testing.T) {
	root := t.TempDir()
	s := newACPServiceAgent(t, root, acptest.Options{}, nil, func(ac *config.AgentConfig) {
		ac.ACP = &config.ACPConfig{Modes: map[string]string{"read_only": acptest.ReadOnlyID}}
	})

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local",
		Prompt: acpTestPrompt, Cwd: ".", TimeoutSec: 30, ReadOnly: true,
	})
	if final.Status != StatusDone {
		t.Fatalf("read-only status = %s (err=%s), want done", final.Status, final.Error)
	}
	if !final.ReadOnly {
		t.Fatalf("job result must record read_only: %+v", final)
	}

	agentLog := readJobLog(t, final, store.StderrFile)
	setAt := strings.Index(agentLog, "session/set_mode mode="+acptest.ReadOnlyID)
	if setAt < 0 {
		t.Fatalf("the agent was never switched to mode %q:\n%s", acptest.ReadOnlyID, agentLog)
	}
	turnAt := strings.Index(agentLog, "session/prompt start")
	if turnAt < 0 || setAt > turnAt {
		t.Fatalf("set_mode must precede the prompt turn (set_at=%d turn_at=%d):\n%s", setAt, turnAt, agentLog)
	}

	lines := readACPJSONL(t, final.ResultDir)
	found := false
	for _, l := range lines {
		if l["t"] == "set_mode" && l["mode"] == acptest.ReadOnlyID {
			found = true
		}
	}
	if !found {
		t.Fatalf("acp.jsonl lacks the set_mode record: %v", lines)
	}
}

// TestACPReadOnlyModeNotOffered: when the agent's session reports availableModes and
// the configured read-only id is not among them, the job FAILS with the offered list —
// the alternative (running the turn in whatever mode the session happens to be in) is
// a writable job the caller believes is read-only.
func TestACPReadOnlyModeNotOffered(t *testing.T) {
	root := t.TempDir()
	s := newACPServiceAgent(t, root, acptest.Options{}, nil, func(ac *config.AgentConfig) {
		ac.ACP = &config.ACPConfig{Modes: map[string]string{"read_only": "nope"}}
	})

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local",
		Prompt: acpTestPrompt, Cwd: ".", TimeoutSec: 30, ReadOnly: true,
	})
	if final.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", final.Status)
	}
	if !strings.Contains(final.Error, `read-only mode "nope" not offered`) {
		t.Fatalf("error = %q, want it to name the missing mode", final.Error)
	}
	if !strings.Contains(final.Error, acptest.ReadOnlyID) {
		t.Fatalf("error = %q, want the agent's available modes listed", final.Error)
	}
	agentLog := readJobLog(t, final, store.StderrFile)
	if strings.Contains(agentLog, "session/set_mode") {
		t.Fatalf("a mode the agent does not offer must not be set:\n%s", agentLog)
	}
	if strings.Contains(agentLog, "session/prompt start") {
		t.Fatalf("the prompt must not run after a rejected read-only mode:\n%s", agentLog)
	}
}

// TestResumeInheritsReadOnly: read-only is a property of the WORK, so a continuation
// inherits it on both resume paths — the exec carrier appends the source agent's
// read-only args to the resume argv, and an acp-agent continuation switches the loaded
// session to the read-only mode. There is deliberately no way to resume into a
// writable job: a writable continuation has to be a NEW job.
func TestResumeInheritsReadOnly(t *testing.T) {
	t.Run("cli-agent exec carrier", func(t *testing.T) {
		root := t.TempDir()
		s := newReadOnlyService(t, root)

		src := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "codex", Runner: "local",
			Prompt: "review the diff", Cwd: ".", TimeoutSec: 30, ReadOnly: true,
			SessionID: "sess-ro",
		})
		if src.Status != StatusDone || !src.ReadOnly {
			t.Fatalf("setup: source = %s read_only=%v, want done/true", src.Status, src.ReadOnly)
		}

		resumed, err := s.ResumeJob(src.ID, "continue the review", "", "caller-ro")
		if err != nil {
			t.Fatalf("ResumeJob: %v", err)
		}
		final, ok := s.Wait(resumed.ID)
		if !ok {
			t.Fatalf("resumed job %s not found", resumed.ID)
		}
		if !resumed.ReadOnly || !final.ReadOnly {
			t.Fatalf("resumed read_only = %v/%v, want true", resumed.ReadOnly, final.ReadOnly)
		}
		if final.Status != StatusDone {
			t.Fatalf("resumed status = %s (err=%s), want done", final.Status, final.Error)
		}
		got := readJobLog(t, final, store.StdoutFile)
		if !strings.Contains(got, "sess-ro") {
			t.Fatalf("the continuation must resume the source session: %q", got)
		}
		if !strings.Contains(got, "\nread-only\n") {
			t.Fatalf("the resume argv must carry the read-only args: %q", got)
		}
	})

	t.Run("acp-agent session/load", func(t *testing.T) {
		root := t.TempDir()
		s := newACPServiceAgent(t, root, acptest.Options{}, nil, func(ac *config.AgentConfig) {
			ac.ACP = &config.ACPConfig{Modes: map[string]string{"read_only": acptest.ReadOnlyID}}
		})

		src := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "acpbot", Runner: "local",
			Prompt: acpTestPrompt, Cwd: ".", TimeoutSec: 30, ReadOnly: true,
		})
		if src.Status != StatusDone || !src.ReadOnly {
			t.Fatalf("setup: source = %s read_only=%v, want done/true", src.Status, src.ReadOnly)
		}

		resumed, err := s.ResumeJob(src.ID, "keep going", "", "caller-ro")
		if err != nil {
			t.Fatalf("ResumeJob: %v", err)
		}
		final, ok := s.Wait(resumed.ID)
		if !ok {
			t.Fatalf("resumed job %s not found", resumed.ID)
		}
		if !resumed.ReadOnly || !final.ReadOnly {
			t.Fatalf("resumed read_only = %v/%v, want true", resumed.ReadOnly, final.ReadOnly)
		}
		agentLog := readJobLog(t, final, store.StderrFile)
		if !strings.Contains(agentLog, "session/load sid="+src.SessionID) {
			t.Fatalf("the continuation must load the source session:\n%s", agentLog)
		}
		if !strings.Contains(agentLog, "session/set_mode mode="+acptest.ReadOnlyID) {
			t.Fatalf("the loaded session must be switched back to read-only:\n%s", agentLog)
		}
	})
}
