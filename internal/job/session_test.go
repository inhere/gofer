package job

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/store"
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

// TestCaptureSessionIDFromFile covers the pure extractor: a hit returns the first
// capture group (trimmed); a miss / missing file / no capture group returns "".
func TestCaptureSessionIDFromFile(t *testing.T) {
	dir := t.TempDir()
	re := `session id:\s*([0-9a-f-]+)`

	hit := filepath.Join(dir, "hit.log")
	if err := os.WriteFile(hit, []byte("starting up\nsession id: 67cc4d00-aaaa-bbbb-cccc-ddddeeeeffff\nrunning\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := captureSessionID(hit, re)
	if got != "67cc4d00-aaaa-bbbb-cccc-ddddeeeeffff" {
		t.Fatalf("captureSessionID hit = %q, want the uuid", got)
	}

	miss := filepath.Join(dir, "miss.log")
	if err := os.WriteFile(miss, []byte("no session line here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := captureSessionID(miss, re); got != "" {
		t.Fatalf("captureSessionID miss = %q, want empty", got)
	}

	// Missing file -> "".
	if got := captureSessionID(filepath.Join(dir, "nope.log"), re); got != "" {
		t.Fatalf("captureSessionID missing-file = %q, want empty", got)
	}
	// Empty inputs -> "".
	if got := captureSessionID("", re); got != "" {
		t.Fatalf("captureSessionID empty-path = %q, want empty", got)
	}
	if got := captureSessionID(hit, ""); got != "" {
		t.Fatalf("captureSessionID empty-regex = %q, want empty", got)
	}
	// Invalid regex -> "" (best-effort, no panic).
	if got := captureSessionID(hit, `session id:\s*([0-9a-f-]+`); got != "" {
		t.Fatalf("captureSessionID invalid-regex = %q, want empty", got)
	}
	// Regex with no capture group -> "".
	if got := captureSessionID(hit, `session id:`); got != "" {
		t.Fatalf("captureSessionID no-group = %q, want empty", got)
	}
}

// newCodexCaptureService builds a Service with a "codex" cli-agent whose command
// prints a `session id: <uuid>` line then exits, so the codex built-in
// SessionCapture regex extracts the id at终态 (capture mode T1.4).
func newCodexCaptureService(t *testing.T, root, sessionID string) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"codex"},
				AllowedRunners: []string{"local"},
			},
		},
		Agents: map[string]config.AgentConfig{
			// command sh -c "echo 'session id: <uuid>'; echo '{{prompt}}'"
			"codex": {Type: agent.TypeCLIAgent, Command: "sh", Args: []string{"-c", "echo 'session id: " + sessionID + "'; echo {{prompt}}"}},
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

// TestCaptureCodexSessionIDAtTerminal proves a codex job (no inject) has its
// session_id captured from stdout at terminal via the built-in regex.
func TestCaptureCodexSessionIDAtTerminal(t *testing.T) {
	root := t.TempDir()
	const sid = "abcd1234-aaaa-bbbb-cccc-001122334455"
	s := newCodexCaptureService(t, root, sid)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "hi", Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done, got %s (err=%s)", final.Status, final.Error)
	}
	if final.SessionID != sid {
		t.Fatalf("captured SessionID = %q, want %q", final.SessionID, sid)
	}
}

func TestCaptureCodexSessionIDFromStderrWhenStdoutMisses(t *testing.T) {
	root := t.TempDir()
	const sid = "abcd1234-aaaa-bbbb-cccc-001122334455"
	s := newCodexCaptureService(t, root, sid)
	resultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(resultDir, store.StdoutFile), []byte("codex started\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resultDir, store.StderrFile), []byte("banner\nsession id: "+sid+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := &jobEntry{result: JobResult{Agent: "codex"}}

	s.captureSession(entry, resultDir)

	entry.mu.Lock()
	got := entry.result.SessionID
	entry.mu.Unlock()
	if got != sid {
		t.Fatalf("captured SessionID from stderr = %q, want %q", got, sid)
	}
}

// TestCaptureMissDoesNotAffectTerminal proves a codex job whose output has no
// session line ends done with an empty session_id (capture is best-effort).
func TestCaptureMissDoesNotAffectTerminal(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"codex"}, AllowedRunners: []string{"local"}},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {Type: agent.TypeCLIAgent, Command: "echo", Args: []string{"{{prompt}}"}},
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
	s := NewService(cfg, projReg, agentReg, runners, meta, nil)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "no-session-line", Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s", final.Status)
	}
	if final.SessionID != "" {
		t.Fatalf("expected empty SessionID on capture miss, got %q", final.SessionID)
	}
}
