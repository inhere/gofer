package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/ptyrelay"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/store"
)

// newPtyCaptureServer builds a server whose "codex" cli-agent carries a
// session_capture regex (the built-in one, resolved by key) so the pty capture
// has something to look for.
func newPtyCaptureServer(t *testing.T) *Server {
	t.Helper()
	return newPtyCaptureServerWithAgents(t, map[string]config.AgentConfig{
		"codex": {Type: agent.TypeCLIAgent, Command: "codex", Args: []string{"{{prompt}}"}},
	})
}

// newPtyCaptureServerWithAgents is newPtyCaptureServer with a caller-chosen agent
// set — every key is allowed in the "self" project — so a capture can also be
// exercised on an agent whose session_capture regex is not codex's.
func newPtyCaptureServerWithAgents(t *testing.T, declared map[string]config.AgentConfig) *Server {
	t.Helper()
	root := t.TempDir()
	allowed := make([]string, 0, len(declared)+1)
	for k := range declared {
		allowed = append(allowed, k)
	}
	allowed = append(allowed, "exec")
	cfg := &config.Config{
		Server:  config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice", CanAttach: true}}},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: allowed, AllowedRunners: []string{"local"}},
		},
		Agents: declared,
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, meta, nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	s := New(&cfg.Server, "", false, jobs, eng, projects, agents, nil, nil, nil, nil)
	s.SetPtyRelay(ptyrelay.NewNonceStore(), ptyrelay.NewRegistry())
	s.SetPtySessionStore(meta)
	return s
}

// upsertPtyJob records a running interactive job whose result dir is returned.
func upsertPtyJob(t *testing.T, s *Server, jobID, agentKey string) string {
	t.Helper()
	// ResultDir is <base>/<job_id> (what serveLog derives the FileStore base from).
	resultDir := filepath.Join(t.TempDir(), jobID)
	if err := os.MkdirAll(resultDir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: jobID, ProjectKey: "self", Agent: agentKey, Runner: "local",
		Interactive: true, Status: "running", Cwd: ".", ResultDir: resultDir,
		RequestJSON: `{"project_key":"self","agent":"` + agentKey + `","runner":"local","interactive":true}`,
		StartedAt:   now, UpdatedAt: now, CallerID: "alice",
	}); err != nil {
		t.Fatalf("upsert pty job: %v", err)
	}
	return resultDir
}

// TestPtySessionIDCapturedFromTail is the PTY-01 §四 regression: a TUI prints its
// session id in the LAST output (codex's exit banner), behind ANSI, after far more
// than the old 64KB head window of noise. The capture must still land the id on
// the job (what `job show` renders).
func TestPtySessionIDCapturedFromTail(t *testing.T) {
	s := newPtyCaptureServer(t)
	upsertPtyJob(t, s, "job-tail", "codex")

	src := newLocalObserverFakeSource()
	done := make(chan struct{})
	go s.runLocalPtyRelay("job-tail", src, done)
	waitForPtyRelay(t, s.ptyRelays, "job-tail", ptyrelay.RelayOpen)

	// ~200KB of ANSI-decorated TUI noise: several times the head window, and the
	// escape sequences straddle the 4KB read boundary.
	line := []byte("\x1b[38;5;240m│ \x1b[0mredrawing the screen \x1b[K\r\n")
	noise := bytes.Repeat(line, (200*1024/len(line))+1)
	src.Emit(noise)

	const sid = "0199f2c1-7a44-7b1e-9f10-2b6c9d0a1e33"
	src.Emit([]byte("\x1b[1mTo continue this session, run \x1b[0mcodex resume " + sid + "\x1b[0m\r\n"))

	waitForLocalObserver(t, 3*time.Second, func() bool {
		got, ok := s.jobs.Get("job-tail")
		return ok && got.SessionID == sid
	})
	src.EOF()
	close(done)
}

// TestFallbackPtyCaptureReadsOnlyTheTailWindow is the AGT-04 live-capture rule: for
// the GENERIC fallback regex only the rolling TAIL window is read, never the frozen
// head one. A fallback banner is an exit banner — it is always at the end — while a
// `--resume <id>` the TUI printed early (echoing a command, explaining its usage)
// sits in the head. The head is checked first, so reading it would record the wrong
// id; ONE observation carries both windows here, which is the only way the two can
// disagree within a single scan.
func TestFallbackPtyCaptureReadsOnlyTheTailWindow(t *testing.T) {
	const sid = "session_hamster_1790079148520_bc5cb0d44153fe56"
	s := newPtyCaptureServerWithAgents(t, map[string]config.AgentConfig{
		"jcode": {Type: agent.TypeCLIAgent, Command: "jcode", InteractiveArgs: []string{}},
	})
	upsertPtyJob(t, s, "job-fb-tail", "jcode")
	ac, _ := s.agents.Get("jcode")

	cap := &ptySessionCapture{srv: s, jobID: "job-fb-tail", agent: "jcode", reSrc: ac.SessionCapture}
	cap.observe([]byte("jcode --resume deadbeefdeadbeef\n" + // decoy: head window
		strings.Repeat("redrawing the screen line\n", 200*1024/26) +
		"jcode --resume " + sid + "\n")) // the real banner: tail window

	got, ok := s.jobs.Get("job-fb-tail")
	if !ok || got.SessionID != sid {
		t.Fatalf("session_id = %q (found=%v), want %q — the fallback must read the tail, not the head", got.SessionID, ok, sid)
	}
	evs, err := s.jobs.ListJobEvents("job-fb-tail", 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	for _, e := range evs {
		if e.Type == job.EventJobSessionCaptured {
			if !strings.Contains(e.Detail, `"by":"fallback"`) || !strings.Contains(e.Detail, `"source":"pty"`) {
				t.Fatalf("event detail = %s, want fallback/pty", e.Detail)
			}
			return
		}
	}
	t.Fatalf("no %s event recorded for the live capture", job.EventJobSessionCaptured)
}

// TestPtyTranscriptWrittenForLocalAndWorkerPty proves the transcript is written at
// BOTH assembly sites with no protocol change (PTY-01 §四): the serve-local pty
// relay and the hub's relay over a worker's pty ws. pty.txt must exist, hold the
// de-ANSI'd text and contain no escape byte.
func TestPtyTranscriptWrittenForLocalAndWorkerPty(t *testing.T) {
	t.Run("local", func(t *testing.T) {
		s := newPtyCaptureServer(t)
		resultDir := upsertPtyJob(t, s, "job-tr-local", "exec")

		src := newLocalObserverFakeSource()
		done := make(chan struct{})
		go s.runLocalPtyRelay("job-tr-local", src, done)
		waitForPtyRelay(t, s.ptyRelays, "job-tr-local", ptyrelay.RelayOpen)

		src.Emit([]byte("\x1b[32mhello\x1b[0m \x1b]0;title\x07world\r\n"))
		src.EOF()
		close(done)

		assertTranscript(t, filepath.Join(resultDir, store.PtyTranscriptFile), "hello world\n")
	})

	t.Run("worker", func(t *testing.T) {
		s, nonces, relays, base, _ := newPtyConnectTestServer(t)
		s.SetPtySessionStore(s.jobs.Meta())
		nonce := preparePtyRelay(t, s, nonces, relays, "job-tr-worker", "pty-tr-worker", ptyTestInst)
		conn := dialPtyAndHello(t, base, ptyConnectHello{JobID: "job-tr-worker", PtySessionID: "pty-tr-worker", RelayNonce: nonce})

		entry := waitForPtyRelay(t, s.ptyRelays, "job-tr-worker", ptyrelay.RelayOpen)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		payload := []byte("\x1b[31mworker\x1b[0m output\r\n")
		if err := conn.Write(ctx, websocket.MessageBinary, payload); err != nil {
			t.Fatalf("write worker binary: %v", err)
		}
		waitRecordedLen(t, entry.Relay, len(payload))

		res, _ := s.jobs.Get("job-tr-worker")
		resultDir := res.ResultDir
		_ = conn.Close(websocket.StatusNormalClosure, "test done")

		assertTranscript(t, filepath.Join(resultDir, store.PtyTranscriptFile), "worker output\n")
	})
}

// assertTranscript waits for the transcript to be sealed and asserts its content:
// de-ANSI'd, CR-folded, and free of ESC bytes.
func assertTranscript(t *testing.T, path, want string) {
	t.Helper()
	var data []byte
	waitForLocalObserver(t, 5*time.Second, func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		data = b
		return len(b) > 0
	})
	if string(data) != want {
		t.Fatalf("pty.txt = %q, want %q", data, want)
	}
	if bytes.ContainsRune(data, 0x1b) {
		t.Fatalf("pty.txt still contains an ESC byte: %q", data)
	}
}

// TestJobLogsFallBackToPtyTranscript pins the read path: an interactive job has no
// stdout.log, so GET /v1/jobs/{id}/logs/stdout serves the pty transcript instead
// of an empty body (CLI `job logs` and the web log page read this endpoint).
func TestJobLogsFallBackToPtyTranscript(t *testing.T) {
	s := newTestServer(t, testToken, false)
	now := time.Now().Unix()
	resultDir := filepath.Join(t.TempDir(), "job-pty-logs")
	if err := os.MkdirAll(resultDir, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := "tui line one\ntui line two\n"
	if err := os.WriteFile(filepath.Join(resultDir, store.PtyTranscriptFile), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: "job-pty-logs", ProjectKey: "self", Agent: "exec", Runner: "local",
		Interactive: true, Status: "done", Cwd: ".", ResultDir: resultDir,
		RequestJSON: `{"project_key":"self","agent":"exec","runner":"local","interactive":true}`,
		StartedAt:   now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert job: %v", err)
	}

	resp := do(t, s, http.MethodGet, "/v1/jobs/job-pty-logs/logs/stdout", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stdout log status=%d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != transcript {
		t.Fatalf("stdout log = %q, want the pty transcript %q", body, transcript)
	}

	// A non-interactive job with a real stdout.log is untouched by the fallback.
	if err := os.WriteFile(filepath.Join(resultDir, store.StdoutFile), []byte("stdout wins\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp = do(t, s, http.MethodGet, "/v1/jobs/job-pty-logs/logs/stdout", testToken, nil)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), "stdout wins") || strings.Contains(string(body), "tui line") {
		t.Fatalf("stdout log = %q, want the real stdout.log to win", body)
	}
}

// TestJobDetailExposesCapturedPtySessionID closes the loop the CLI/user sees: the
// id captured from the pty tail is what `job show`/the API report, and the job's
// request_json keeps saying it was interactive (no schema drift).
func TestJobDetailExposesCapturedPtySessionID(t *testing.T) {
	s := newPtyCaptureServer(t)
	upsertPtyJob(t, s, "job-detail-pty", "codex")

	const sid = "0199f2c1-7a44-7b1e-9f10-2b6c9d0a1e33"
	s.jobs.SetSessionID("job-detail-pty", sid)
	res, ok := s.jobs.Get("job-detail-pty")
	if !ok || res.SessionID != sid || !res.Interactive {
		t.Fatalf("job = %+v ok=%v, want session_id %s on an interactive job", res, ok, sid)
	}
	var req job.JobRequest
	if err := json.Unmarshal([]byte(res.RequestJSON), &req); err != nil || !req.Interactive {
		t.Fatalf("request_json interactive lost: %v (%s)", err, res.RequestJSON)
	}
}
