package job

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/ptyrelay"
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
	return drainOnClose(t, NewService(cfg, projReg, agentReg, runners, meta, nil))
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
	drainJobs(t, s)
}

// submitDebugContext adds the context both failure branches of
// TestSubmitExecNoSessionInjection print (bd h-aii-pq8a): the full Submit error,
// how many goroutines were alive and how many jobs the service still tracks. The
// assertions themselves are unchanged — this only makes a flake (h-aii-3cdc)
// diagnosable from the test log alone.
func submitDebugContext(s *Service) string {
	s.mu.Lock()
	active := len(s.jobs)
	s.mu.Unlock()
	return fmt.Sprintf("goroutines=%d active_jobs=%d", runtime.NumGoroutine(), active)
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
		t.Fatalf("Submit: %v (%s)", err, submitDebugContext(s))
	}
	if res.SessionID != "" {
		t.Fatalf("exec job should not inject a session_id, got %q (%s)", res.SessionID, submitDebugContext(s))
	}
	drainJobs(t, s)
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

func TestCaptureSessionIDBytes(t *testing.T) {
	const sid = "67cc4d00-aaaa-bbbb-cccc-ddddeeeeffff"
	got := CaptureSessionIDBytes([]byte("banner\nsession id: "+sid+"\n"), `session id:\s*([0-9a-f-]+)`)
	if got != sid {
		t.Fatalf("CaptureSessionIDBytes = %q, want %q", got, sid)
	}
	if miss := CaptureSessionIDBytes([]byte("no session"), `session id:\s*([0-9a-f-]+)`); miss != "" {
		t.Fatalf("CaptureSessionIDBytes miss = %q, want empty", miss)
	}
}

// TestCaptureSessionIDFirstNonEmptyGroup pins the extractor contract behind a
// multi-branch capture regex (PTY-01 F6): each alternative owns a group and only ONE
// of them fires per match, so the id is the first NON-EMPTY group — which is what an
// interactive omp job needs, since its id comes from the second branch (the TUI exit
// banner) and the first branch's group is empty there. Taking group 1 blindly returned
// "" and left `job resume` unusable.
//
// The regex is the omp built-in as the agent registry resolves it (ndjson session row
// first, TUI exit banner second).
func TestCaptureSessionIDFirstNonEmptyGroup(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"omp": {Type: agent.TypeCLIAgent, Command: "omp"},
	}}
	ac, ok := agent.NewRegistry(cfg).Get("omp")
	if !ok || ac.SessionCapture == "" {
		t.Fatalf("omp built-in session capture missing: ok=%v agent=%#v", ok, ac)
	}
	const sid = "01a0c84d-d444-72ca-ad7c-edadbae32034"

	// Branch 1 (ndjson session row) fires -> the id sits in group 1.
	if got := CaptureSessionIDBytes([]byte(`{"type":"session","id":"`+sid+`"}`), ac.SessionCapture); got != sid {
		t.Fatalf("ndjson branch capture = %q, want %q", got, sid)
	}
	// Branch 2 (TUI exit banner) fires -> group 1 is EMPTY, the id sits in group 2.
	line := []byte("Resume this session with omp --resume " + sid + "\r\n")
	if got := CaptureSessionIDBytes(line, ac.SessionCapture); got != sid {
		t.Fatalf("tui-exit branch capture = %q, want %q (first non-empty group, not group 1)", got, sid)
	}
	// Neither branch produced an id -> "" (never the empty group of the other branch).
	if got := CaptureSessionIDBytes([]byte("Resume this session with omp --resume\n"), ac.SessionCapture); got != "" {
		t.Fatalf("miss capture = %q, want empty", got)
	}
	// A union regex whose groups are ALL empty -> "" (no false positive from a match).
	if got := CaptureSessionIDBytes([]byte("nothing here"), `(x)?(y)?`); got != "" {
		t.Fatalf("all-empty-groups capture = %q, want empty", got)
	}
}

// TestCaptureSessionIDFromPaddedTranscript composes both F6 fixes over the real chain
// (pty bytes -> the relay's de-ANSI filter -> the capture regex): the TUI prints its
// exit banner through the same renderer that pads columns with CUF, so the regexes only
// see the id once the padding has become whitespace. Pre-fix the line read
// `claude--resume<uuid>` / `omp--resume<uuid>` and nothing could match it.
func TestCaptureSessionIDFromPaddedTranscript(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"omp":    {Type: agent.TypeCLIAgent, Command: "omp"},
		"claude": {Type: agent.TypeCLIAgent, Command: "claude"},
	}}
	reg := agent.NewRegistry(cfg)

	const claudeSID = "7c4418ff-0928-4e33-8347-c24241d919c0"
	const ompSID = "01a0c84d-d444-72ca-ad7c-edadbae32034"
	cases := []struct {
		agentKey string
		raw      string
		wantText string
		wantID   string
	}{
		{
			agentKey: "claude",
			raw: "New\x1b[1CMCP\x1b[3Cserver\x1b[1Cfound\r\n" +
				"Resume\x1b[1Cthis\x1b[1Csession\x1b[1Cwith:\r\n" +
				"claude\x1b[1C--resume\x1b[1C" + claudeSID + "\r\n",
			wantText: "New MCP   server found\n" +
				"Resume this session with:\n" +
				"claude --resume " + claudeSID + "\n",
			wantID: claudeSID,
		},
		{
			agentKey: "omp",
			raw: "\x1b[<u\x1b[>4;0mResume\x1b[1Cthis\x1b[1Csession\x1b[1Cwith\x1b[1Comp" +
				"\x1b[1C--resume\x1b[1C" + ompSID + "\x1b[0m\r\n",
			wantText: "Resume this session with omp --resume " + ompSID + "\n",
			wantID:   ompSID,
		},
	}
	for _, tc := range cases {
		ac, ok := reg.Get(tc.agentKey)
		if !ok || ac.SessionCapture == "" {
			t.Fatalf("%s: no session capture in the resolved agent (ok=%v)", tc.agentKey, ok)
		}
		var s ptyrelay.Stripper
		text := string(s.Write([]byte(tc.raw)))
		if text != tc.wantText {
			t.Fatalf("%s: transcript = %q, want %q", tc.agentKey, text, tc.wantText)
		}
		if got := CaptureSessionIDBytes([]byte(text), ac.SessionCapture); got != tc.wantID {
			t.Fatalf("%s: captured session id = %q, want %q", tc.agentKey, got, tc.wantID)
		}
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
	return drainOnClose(t, NewService(cfg, projReg, agentReg, runners, meta, nil))
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
	s := drainOnClose(t, NewService(cfg, projReg, agentReg, runners, meta, nil))

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

// newInteractiveClaudeInjectService builds a Service with an INTERACTIVE
// cli-agent ("tty-claude" shape: interactive_args, no_raw_cmd, session_inject) so
// the T1.3 question — does the inject path also cover the TUI argv? — is answered
// against the real configuration shape. The command is the harmless `echo`, so the
// job completes without a real claude CLI.
func newInteractiveClaudeInjectService(t *testing.T, root string) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:         root,
				AllowedAgents:    []string{"tty-claude"},
				AllowedRunners:   []string{"local"},
				AllowInteractive: boolPtr(true),
			},
		},
		Agents: map[string]config.AgentConfig{
			"tty-claude": {
				Type: agent.TypeCLIAgent, Command: "echo", Args: []string{"{{prompt}}"},
				Interactive: true, InteractiveArgs: []string{}, NoRawCmd: true,
				SessionInject: []string{"--session-id", "{{session_id}}"},
			},
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
	return drainOnClose(t, NewService(cfg, projReg, agentReg, runners, meta, nil))
}

// TestInteractiveBuildInjectsSessionID proves an INTERACTIVE job gets the agent's
// session_inject appended to its TUI argv (claude's `--session-id <uuid>` works in
// TUI mode too) and the id bound at submit time — which is what makes
// `job resume` able to continue an interactive session (PTY-01 §四). The
// agent's no_raw_cmd flag does NOT refuse it: that flag guards CALLER-supplied
// argv, not gofer's own injection.
func TestInteractiveBuildInjectsSessionID(t *testing.T) {
	root := t.TempDir()
	s := newInteractiveClaudeInjectService(t, root)

	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "tty-claude", Runner: "local",
		Interactive: true, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("Submit interactive: %v", err)
	}
	if !uuidV4Re.MatchString(res.SessionID) {
		t.Fatalf("interactive SessionID = %q, want an injected v4 UUID", res.SessionID)
	}

	final, _ := s.Wait(res.ID)
	if final.SessionID != res.SessionID {
		t.Fatalf("SessionID changed after run: %q != %q", final.SessionID, res.SessionID)
	}
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
		t.Fatalf("interactive argv missing injected --session-id %q: %#v", res.SessionID, rc.Args)
	}
}

// TestCaptureOutcomesScansPtyTranscript proves the终态 fallback (PTY-01 §四): an
// interactive job's pty output never enters stdout/stderr, so a session id that
// only exists in the de-ANSI'd pty.txt (codex's TUI exit banner) is still bound to
// the job. A non-interactive job must NOT read the transcript.
func TestCaptureOutcomesScansPtyTranscript(t *testing.T) {
	root := t.TempDir()
	const sid = "0199f2c1-7a44-7b1e-9f10-2b6c9d0a1e33"
	s := newCodexCaptureService(t, root, "unused-nothing-line")

	resultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(resultDir, store.StdoutFile), []byte("no session line here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The TUI exit banner form, already de-ANSI'd by the relay's transcript.
	transcript := "╭─ codex ─╮\nbye\nTo continue this session, run codex resume " + sid + "\n"
	if err := os.WriteFile(filepath.Join(resultDir, store.PtyTranscriptFile), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}

	entry := &jobEntry{result: JobResult{Agent: "codex", Interactive: true}}
	s.captureSession(entry, resultDir)
	entry.mu.Lock()
	got := entry.result.SessionID
	entry.mu.Unlock()
	if got != sid {
		t.Fatalf("interactive capture from pty.txt = %q, want %q", got, sid)
	}

	// Batch job: the transcript is not a source for a non-interactive run.
	batch := &jobEntry{result: JobResult{Agent: "codex"}}
	s.captureSession(batch, resultDir)
	batch.mu.Lock()
	gotBatch := batch.result.SessionID
	batch.mu.Unlock()
	if gotBatch != "" {
		t.Fatalf("non-interactive capture read pty.txt = %q, want empty", gotBatch)
	}
}
