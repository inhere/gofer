package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// newAgentService builds a Service whose "self" project allows exactly the given
// agents (plus exec). Each agent's Command is `echo`, so a job's stdout is its own
// rendered argv — enough to drive the session capture without a real agent CLI.
func newAgentService(t *testing.T, root string, agents map[string]config.AgentConfig) *Service {
	t.Helper()
	allowed := make([]string, 0, len(agents)+1)
	for k := range agents {
		allowed = append(allowed, k)
	}
	allowed = append(allowed, "exec")
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  allowed,
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: agents,
	}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	return drainOnClose(t, NewService(cfg, project.NewRegistry(cfg, ""), agent.NewRegistry(cfg),
		map[string]runner.Runner{localrunner.Name: localrunner.New()}, meta, nil))
}

// TestFallbackCaptureOnlyScansTail: the generic fallback reads only the LAST 4KB of
// a log (AGT-04 误抓防护) — the exit banner is always at the tail, while a `--resume
// <id>` the agent merely TALKED about lands mid-body. The same regex over the whole
// file would take the mid-body hit (leftmost), so the two assertions together pin
// the window, not just the match.
func TestFallbackCaptureOnlyScansTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stdout.log")
	filler := strings.Repeat("redrawing the screen line\n", 200*1024/26)
	body := filler +
		"foo --resume deadbeefdeadbeef\n" + // decoy: >4KB from EOF
		strings.Repeat("redrawing the screen line\n", 8*1024/26) +
		"foo --resume session_x_123456789\n" // the real banner, inside the tail window
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := captureSessionID(path, agent.FallbackSessionCapture); got != "session_x_123456789" {
		t.Fatalf("captureSessionID = %q, want the tail id %q (the mid-body decoy must be outside the window)", got, "session_x_123456789")
	}
	// Control: the identical regex over the WHOLE file takes the leftmost (mid-body)
	// hit, so the tail-only read above is what produced the right answer.
	if got := CaptureSessionIDBytes([]byte(body), agent.FallbackSessionCapture); got != "deadbeefdeadbeef" {
		t.Fatalf("full-text capture = %q, want the mid-body decoy %q", got, "deadbeefdeadbeef")
	}
}

// TestFallbackCaptureRejectsPlaceholders: the fallback regex cannot express "not a
// placeholder" (AGT-04: 正则难表达), so a post-capture guard drops them. Everything
// here is either token-shaped enough to match the regex or too short to match at
// all; both must end up as "no capture" rather than a bogus `job resume --resume
// <session_id>`.
func TestFallbackCaptureRejectsPlaceholders(t *testing.T) {
	dir := t.TempDir()
	for i, bad := range []string{
		"--resume <session_id>",
		"--resume SESSION_ID",
		`--resume "SESSION_ID"`,
		"--resume session-id",
		"--resume sessionid",
		"--resume your-session-id",
		"--resume ...",
		"--resume -",
		"--resume none",
		"--resume null",
		"--resume uuid",
		"--resume xxx",
	} {
		path := filepath.Join(dir, "placeholder-"+string(rune('a'+i))+".log")
		if err := os.WriteFile(path, []byte("bye\n"+bad+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := captureSessionID(path, agent.FallbackSessionCapture); got != "" {
			t.Errorf("%q captured %q, want empty", bad, got)
		}
	}
}

// TestFallbackCaptureRecordsEvent pins the audit trail: a captured session id
// records job.session_captured with {agent, by, source}, where `by` says whether the
// id came from the agent's OWN session_capture (built-in or configured) or from
// gofer's generic fallback — the signal that says "this agent deserves a regex".
func TestFallbackCaptureRecordsEvent(t *testing.T) {
	const jcodeID = "session_hamster_1790079148520_bc5cb0d44153fe56"

	t.Run("fallback", func(t *testing.T) {
		s := newAgentService(t, t.TempDir(), map[string]config.AgentConfig{
			"jcode": {Type: agent.TypeCLIAgent, Command: "echo", Args: []string{"--resume", jcodeID, "{{prompt}}"}},
		})
		final := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "jcode", Runner: "local", Prompt: "hi", Cwd: ".", TimeoutSec: 30,
		})
		if final.Status != StatusDone {
			t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
		}
		if final.SessionID != jcodeID {
			t.Fatalf("session_id = %q, want %q", final.SessionID, jcodeID)
		}
		assertSessionCapturedEvent(t, s, final.ID, `"agent":"jcode"`, `"by":"fallback"`, `"source":"stdout"`)
	})

	t.Run("agent_config", func(t *testing.T) {
		s := newAgentService(t, t.TempDir(), map[string]config.AgentConfig{
			"mycap": {
				Type: agent.TypeCLIAgent, Command: "echo",
				SessionCapture: `sid=(\S+)`,
				Args:           []string{"sid=abc12345", "{{prompt}}"},
			},
		})
		final := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "mycap", Runner: "local", Prompt: "hi", Cwd: ".", TimeoutSec: 30,
		})
		if final.SessionID != "abc12345" {
			t.Fatalf("session_id = %q, want %q", final.SessionID, "abc12345")
		}
		assertSessionCapturedEvent(t, s, final.ID, `"by":"agent_config"`, `"source":"stdout"`)
	})

	t.Run("builtin", func(t *testing.T) {
		const codexID = "0199f2c1-7a44-7b1e-9f10-2b6c9d0a1e33"
		s := newAgentService(t, t.TempDir(), map[string]config.AgentConfig{
			"codex": {
				Type: agent.TypeCLIAgent, Command: "echo",
				Args: []string{"session", "id:", codexID, "{{prompt}}"},
			},
		})
		final := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "codex", Runner: "local", Prompt: "hi", Cwd: ".", TimeoutSec: 30,
		})
		if final.SessionID != codexID {
			t.Fatalf("session_id = %q, want %q", final.SessionID, codexID)
		}
		assertSessionCapturedEvent(t, s, final.ID, `"by":"agent_config"`, `"source":"stdout"`)
	})
}

// assertSessionCapturedEvent finds the job's job.session_captured event and checks
// its detail carries every wanted fragment.
func assertSessionCapturedEvent(t *testing.T, s *Service, jobID string, wants ...string) {
	t.Helper()
	evs, err := s.ListJobEvents(jobID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	for _, e := range evs {
		if e.Type != EventJobSessionCaptured {
			continue
		}
		for _, want := range wants {
			if !strings.Contains(e.Detail, want) {
				t.Fatalf("event detail = %s, want it to contain %s", e.Detail, want)
			}
		}
		return
	}
	t.Fatalf("no %s event in %v", EventJobSessionCaptured, eventTypes(t, s, jobID))
}
