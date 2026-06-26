package job

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// newClaudeInjectService builds a Service with a "claude" cli-agent whose command
// is the harmless `echo` (so the job runs without a real claude CLI). The agent
// declares no session fields, so the built-in claude defaults (SessionInject
// --session-id {{session_id}}) apply — exactly the inject path T1.3 exercises.
func newClaudeInjectService(t *testing.T, root string) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"claude", "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"claude": {Type: agent.TypeCLIAgent, Command: "echo", Args: []string{"{{prompt}}"}},
		},
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	return NewService(cfg, projReg, agentReg, runners, meta, nil)
}

// uuidV4Re matches a canonical RFC 4122 version-4 UUID (version nibble 4, variant
// nibble one of 8/9/a/b).
var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestNewUUIDIsValidV4 proves newUUID emits a syntactically valid v4 UUID
// (claude's --session-id requires a legal UUID).
func TestNewUUIDIsValidV4(t *testing.T) {
	for i := 0; i < 100; i++ {
		u := newUUID()
		if !uuidV4Re.MatchString(u) {
			t.Fatalf("newUUID() = %q is not a valid v4 UUID", u)
		}
	}
}

// TestNewUUIDIsUnique proves two calls do not collide (random source).
func TestNewUUIDIsUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		u := newUUID()
		if _, dup := seen[u]; dup {
			t.Fatalf("newUUID() produced a duplicate: %q", u)
		}
		seen[u] = struct{}{}
	}
}

// TestSubmitInjectsSessionIDForClaude proves a claude job (SessionInject default)
// gets a session_id generated and bound at submit time — immediately, without
// waiting for output — and that the SAME id is what was injected into argv
// (visible in the rendered command after the job finishes).
func TestSubmitInjectsSessionIDForClaude(t *testing.T) {
	root := t.TempDir()
	s := newClaudeInjectService(t, root)

	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local",
		Prompt: "hello", Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// Immediately (queued/running) the session_id is already present (inject mode).
	if !uuidV4Re.MatchString(res.SessionID) {
		t.Fatalf("injected SessionID = %q is not a valid v4 UUID", res.SessionID)
	}

	final, _ := s.Wait(res.ID)
	if final.SessionID != res.SessionID {
		t.Fatalf("SessionID changed after run: %q != %q", final.SessionID, res.SessionID)
	}
	// The injected id is the one that was appended to argv (--session-id <uuid>).
	var rc struct {
		Args []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(final.RenderedCommand), &rc); err != nil {
		t.Fatalf("RenderedCommand not valid JSON: %v (%q)", err, final.RenderedCommand)
	}
	var sawFlag, sawID bool
	for _, a := range rc.Args {
		if a == "--session-id" {
			sawFlag = true
		}
		if a == res.SessionID {
			sawID = true
		}
	}
	if !sawFlag || !sawID {
		t.Fatalf("argv missing injected --session-id %q: %#v", res.SessionID, rc.Args)
	}
}

// TestSubmitExplicitSessionIDWins proves a request-supplied SessionID (resume
// path) is used verbatim and is not replaced by an injected uuid.
func TestSubmitExplicitSessionIDWins(t *testing.T) {
	root := t.TempDir()
	s := newClaudeInjectService(t, root)

	const sid = "11111111-2222-4333-8444-555555555555"
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local",
		Prompt: "hi", Cwd: ".", TimeoutSec: 30, SessionID: sid,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.SessionID != sid {
		t.Fatalf("explicit SessionID not honoured: got %q want %q", res.SessionID, sid)
	}
}

// TestSubmitExecNoSessionInjection proves a plain exec job (no SessionInject)
// carries no session_id at submit time (codex/exec are capture or none).
func TestSubmitExecNoSessionInjection(t *testing.T) {
	root := t.TempDir()
	s := newClaudeInjectService(t, root)

	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.SessionID != "" {
		t.Fatalf("exec job should not inject a session_id, got %q", res.SessionID)
	}
}
