package job

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// f11SessionID is the session id every F11 test uses: the omp session row of the
// job the user reported (20260924-133603-319a66ce), whose row was left with an
// empty session_id by a serve restart.
const f11SessionID = "01a0d1e9-eb4f-70c7-ae02-9bfbe18a0404"

// f11SessionLine is the omp `session` row the ndjson projector reads its id from.
func f11SessionLine() string {
	return `{"type":"session","id":"` + f11SessionID + `","model":"omp-1","cwd":"."}`
}

// f11OmpService builds a service whose "omp" agent resolves the built-in omp
// session capture (and resume template) over the given agent config.
func f11OmpService(t *testing.T, root string, ac config.AgentConfig) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"omp", "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true, // resume submits an exec carrier
			},
		},
		Agents: map[string]config.AgentConfig{"omp": ac},
	}
	return newServiceFromCfg(t, root, cfg)
}

// waitJobSessionID polls the PERSISTED job row until it carries a session id (or the
// deadline passes), so the assertion sees the row as a reader would — a fresh serve
// process (or `job show`) reads the DB, never the in-process entry.
func waitJobSessionID(t *testing.T, s *Service, jobID string, timeout time.Duration) jobstore.JobRecord {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		rec, ok, err := s.meta.GetJob(jobID)
		if err != nil {
			t.Fatalf("GetJob(%s): %v", jobID, err)
		}
		if !ok {
			t.Fatalf("job %s not in the store", jobID)
		}
		if rec.SessionID != "" {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never persisted a session_id (status=%s)", jobID, rec.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// seedOrphanJob writes the state a serve restart leaves behind: a NON-terminal job
// row with an empty session_id, plus one log file under its result dir.
func seedOrphanJob(t *testing.T, s *Service, root, jobID, logFile, body string, interactive bool) {
	t.Helper()
	dir := filepath.Join(root, "self", jobID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir result dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, logFile), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", logFile, err)
	}
	now := s.nowFn().Unix()
	rec := jobstore.JobRecord{
		ID: jobID, ProjectKey: "self", Agent: "omp", Runner: "local",
		Interactive: interactive,
		Status:      StatusRunning, Cwd: ".", ResultDir: dir,
		StartedAt: now - 10, UpdatedAt: now,
	}
	if err := s.meta.UpsertJob(rec); err != nil {
		t.Fatalf("seed %s: %v", jobID, err)
	}
}

// TestNDJSONSessionPersistedWhenSeen is the F11 core: an ndjson agent's session id
// reaches the job ROW the moment the stream carries it, not only at finish(). The
// fake stream prints the omp session row and then keeps running, so the read below
// happens while the job is still live — exactly the window a serve restart kills
// (the row is then failed by ReconcileOrphanJobs, and a session id that only ever
// lived in the in-process entry is gone, leaving `job resume` unable to continue it).
func TestNDJSONSessionPersistedWhenSeen(t *testing.T) {
	root := t.TempDir()
	s := f11OmpService(t, root, config.AgentConfig{
		Type:    agent.TypeCLIAgent,
		Command: testcmd.Path(t),
		// stdout-sleep prints the session row, then keeps the job running: the id has
		// to be readable long before the process exits.
		Args:         []string{"stdout-sleep", f11SessionLine(), "5s"},
		OutputFormat: config.OutputFormatNDJSON,
	})

	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "omp", Runner: "local",
		Prompt: "hi", Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(res.ID); s.Wait(res.ID) })

	rec := waitJobSessionID(t, s, res.ID, 10*time.Second)
	if rec.SessionID != f11SessionID {
		t.Fatalf("persisted session_id = %q, want %q", rec.SessionID, f11SessionID)
	}
	if rec.Status != StatusRunning {
		t.Fatalf("session_id landed with status=%s, want it persisted WHILE the job runs (finish must not be required)", rec.Status)
	}
	assertSessionCapturedEvent(t, s, res.ID, `"agent":"omp"`, `"by":"agent_config"`, `"source":"ndjson"`)
}

// TestOrphanReconcileCapturesSessionFromLogs: the restart path itself. A job left
// non-terminal by a dead serve is failed by ReconcileOrphanJobs — and before that
// flip its logs are scanned for a session id, so the failed row is still resumable.
// All three places an id can live are covered: an ndjson agent's row is normally in
// stdout.log (final answer) / stderr.log (event stream), and an interactive job's
// only in the de-ANSI'd pty transcript.
func TestOrphanReconcileCapturesSessionFromLogs(t *testing.T) {
	cases := []struct {
		name        string
		file        string
		interactive bool
		body        string
	}{
		{"stdout", store.StdoutFile, false, f11SessionLine() + "\n"},
		{"stderr", store.StderrFile, false, f11SessionLine() + "\n"},
		// An interactive job's output never reaches stdout/stderr, so its id is only
		// in the pty transcript (the omp TUI prints it on the way out).
		{"pty", store.PtyTranscriptFile, true, "Resume this session with omp --resume " + f11SessionID + "\r\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			s := f11OmpService(t, root, config.AgentConfig{
				Type: agent.TypeCLIAgent, Command: "omp", Args: []string{"-p", "{{prompt}}"},
			})
			seedOrphanJob(t, s, root, "j-orphan", tc.file, tc.body, tc.interactive)

			if _, err := s.ReconcileOrphanJobs(); err != nil {
				t.Fatalf("ReconcileOrphanJobs: %v", err)
			}

			rec, ok, err := s.meta.GetJob("j-orphan")
			if err != nil || !ok {
				t.Fatalf("GetJob: ok=%v err=%v", ok, err)
			}
			if rec.Status != StatusFailed {
				t.Fatalf("status = %s, want failed (a local job cannot survive a restart)", rec.Status)
			}
			if rec.SessionID != f11SessionID {
				t.Fatalf("session_id = %q, want %q recovered from %s", rec.SessionID, f11SessionID, tc.file)
			}
			assertSessionCapturedEvent(t, s, "j-orphan", `"agent":"omp"`, `"by":"agent_config"`, `"source":"orphan_scan"`)
		})
	}
}

// TestOrphanedJobIsResumable closes the loop the user reported: a job killed by a
// serve restart used to end `failed` with an empty session_id, so `job resume` — the
// one action that recovers the work — refused it (ErrNoSession). After the reconcile
// captures the id from the logs, the resume goes through and its argv continues the
// SAME agent session.
func TestOrphanedJobIsResumable(t *testing.T) {
	root := t.TempDir()
	// Command is the test helper, not a real `omp`: the resumed carrier must not
	// launch an agent CLI on the machine running the tests.
	s := f11OmpService(t, root, config.AgentConfig{
		Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"-p", "{{prompt}}"},
	})
	seedOrphanJob(t, s, root, "j-orphan", store.StderrFile, f11SessionLine()+"\n", false)

	if _, err := s.ReconcileOrphanJobs(); err != nil {
		t.Fatalf("ReconcileOrphanJobs: %v", err)
	}
	src, _, _ := s.meta.GetJob("j-orphan")
	if src.SessionID != f11SessionID {
		t.Fatalf("setup: reconcile did not recover the session id, got %q", src.SessionID)
	}

	resumed, err := s.ResumeJob("j-orphan", "继续", "", "caller-1")
	if err != nil {
		t.Fatalf("ResumeJob after an orphan reconcile: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(resumed.ID); s.Wait(resumed.ID) })

	if resumed.SessionID != f11SessionID {
		t.Fatalf("resumed session_id = %q, want %q (the continuation binds to the source session)", resumed.SessionID, f11SessionID)
	}
	argv := resumeArgv(t, resumed.RequestJSON)
	if !containsArg(argv, "--resume") || !containsArg(argv, f11SessionID) {
		t.Fatalf("resumed argv = %#v, want it to carry `--resume %s`", argv, f11SessionID)
	}
}
