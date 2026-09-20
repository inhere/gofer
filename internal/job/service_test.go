package job

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/store"
)

// newTestService builds a Service whose result base dir lives under a temp dir.
// It registers two projects: "self" (allow_exec=true) and "noexec"
// (allow_exec=false). storage.root points at root so result dirs are isolated.
// The metadata db lives under root so each test gets its own DB.
func newTestService(t *testing.T, root string) *Service {
	t.Helper()
	return newTestServiceWithDB(t, root, filepath.Join(root, "gofer.db"))
}

// newTestServiceWithDB is like newTestService but opens the metadata store at an
// explicit dbPath. Tests that simulate a restart (a fresh Service that must still
// see jobs persisted by an earlier one) pass the same dbPath to both services.
func newTestServiceWithDB(t *testing.T, root, dbPath string) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root, // any existing dir; cwd "." resolves here
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
			"noexec": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      false,
			},
		},
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	meta, err := jobstore.Open(dbPath)
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	return drainOnClose(t, NewService(cfg, projReg, agentReg, runners, meta, nil))
}

func submitAndWait(t *testing.T, s *Service, req JobRequest) JobResult {
	t.Helper()
	res, err := s.Submit(req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final, ok := s.Wait(res.ID)
	if !ok {
		t.Fatalf("Wait: job %s not found", res.ID)
	}
	return final
}

func TestSubmitExecDone(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s (err=%s)", final.Status, final.Error)
	}
	if final.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", final.ExitCode)
	}
	out, err := store.NewFileStore(filepath.Join(root, "self")).ReadLogTail(final.ID, store.StreamStdout, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "go version") {
		t.Fatalf("stdout.log missing output: %q", out)
	}
}

func TestSubmitExecFailed(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "exit 3"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusFailed {
		t.Fatalf("expected failed, got %s", final.Status)
	}
	if final.ExitCode != 3 {
		t.Fatalf("expected exit 3, got %d", final.ExitCode)
	}
}

func TestSubmitTimeout(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "5"}, Cwd: ".", TimeoutSec: 1,
	})
	if final.Status != StatusTimeout {
		t.Fatalf("expected timeout, got %s (err=%s)", final.Status, final.Error)
	}
}

func TestNormalizeTimeoutInteractiveUnsetMeansNoDeadline(t *testing.T) {
	if got, _ := normalizeTimeout(0, true, false, 0); got != 0 {
		t.Fatalf("interactive unset timeout = %s, want no deadline", got)
	}
	if got, _ := normalizeTimeout(1, true, false, 0); got != time.Second {
		t.Fatalf("interactive explicit timeout = %s, want 1s", got)
	}
	if got, _ := normalizeTimeout(0, false, false, 0); got != DefaultTimeoutSec*time.Second {
		t.Fatalf("non-interactive unset timeout = %s, want default", got)
	}
	if got, _ := normalizeTimeout(0, false, true, 0); got != DefaultAgentTimeoutSec*time.Second {
		t.Fatalf("cli-agent unset timeout = %s, want agent default", got)
	}
	if got, _ := normalizeTimeout(30, false, true, 0); got != 30*time.Second {
		t.Fatalf("cli-agent explicit timeout = %s, want 30s (explicit wins)", got)
	}
}

// TestNormalizeTimeoutUsesConfiguredCeiling: the clamp ceiling is the caller-
// resolved, per-project value (bd h-aii-s9ck) rather than a hard-coded 1h, and a
// truncated REQUEST is reported so the caller can say so instead of silently
// running a shorter job. A default that merely exceeds the ceiling is NOT a clamp
// (the caller never asked for that value).
func TestNormalizeTimeoutUsesConfiguredCeiling(t *testing.T) {
	cases := []struct {
		name        string
		sec         int
		interactive bool
		cliAgent    bool
		max         int
		want        time.Duration
		wantClamped bool
	}{
		{"under the ceiling", 60, false, false, 7200, 60 * time.Second, false},
		{"ceiling raised above the old 1h", 5400, false, false, 7200, 5400 * time.Second, false},
		{"request above the ceiling is clamped", 5400, false, false, 3600, 3600 * time.Second, true},
		{"project ceiling below the default", 1200, false, false, 120, 120 * time.Second, true},
		{"unset timeout takes the default, not the ceiling", 0, false, false, 3600, DefaultTimeoutSec * time.Second, false},
		{"default above a low ceiling is not a request clamp", 0, false, false, 120, 120 * time.Second, false},
		{"no configured ceiling falls back to the built-in one", 9999, false, false, 0, DefaultMaxTimeoutSec * time.Second, true},
		{"interactive unset stays unbounded even with a ceiling", 0, true, false, 600, 0, false},
		{"interactive explicit above the ceiling is clamped", 5400, true, false, 600, 600 * time.Second, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, clamped := normalizeTimeout(tc.sec, tc.interactive, tc.cliAgent, tc.max)
			if got != tc.want {
				t.Errorf("normalizeTimeout(%d, interactive=%v, cli=%v, max=%d) = %s, want %s",
					tc.sec, tc.interactive, tc.cliAgent, tc.max, got, tc.want)
			}
			if clamped != tc.wantClamped {
				t.Errorf("normalizeTimeout(%d, …, max=%d) clamped = %v, want %v",
					tc.sec, tc.max, clamped, tc.wantClamped)
			}
		})
	}
}

// withProjectCeiling returns a copy of cfg with projectKey's job-timeout ceiling
// pinned, leaving the original untouched (shallow Config copy + copied project map).
func withProjectCeiling(cfg *config.Config, projectKey string, ceiling int) *config.Config {
	out := *cfg
	out.Projects = maps.Clone(cfg.Projects)
	p := out.Projects[projectKey]
	p.MaxTimeoutSec = ceiling
	out.Projects[projectKey] = p
	return &out
}

// TestSubmitReportsTimeoutClamp: a request above the project ceiling is admitted at
// the ceiling, and the response says so (effective + requested + clamped) instead of
// silently truncating; the three values survive the DB round trip so `job show`/GET
// can explain a job's early death after a server restart (bd h-aii-s9ck).
func TestSubmitReportsTimeoutClamp(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	s.Reload(withProjectCeiling(s.config(), "self", 120))

	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 5400,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.TimeoutSec != 120 {
		t.Errorf("effective timeout_sec = %d, want 120 (the project ceiling)", res.TimeoutSec)
	}
	if res.RequestedTimeoutSec != 5400 {
		t.Errorf("requested_timeout_sec = %d, want 5400 (what the caller asked for)", res.RequestedTimeoutSec)
	}
	if !res.TimeoutClamped {
		t.Error("timeout_clamped = false, want true (the request exceeded the ceiling)")
	}

	rec, ok, err := s.meta.GetJob(res.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob(%s): ok=%v err=%v", res.ID, ok, err)
	}
	got := fromRecord(rec)
	if got.TimeoutSec != 120 || got.RequestedTimeoutSec != 5400 || !got.TimeoutClamped {
		t.Errorf("persisted timeout triple = (%d, %d, %v), want (120, 5400, true)",
			got.TimeoutSec, got.RequestedTimeoutSec, got.TimeoutClamped)
	}

	// A request under the ceiling is reported as-is, unclamped.
	res, err = s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 60,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.TimeoutSec != 60 || res.TimeoutClamped {
		t.Errorf("under-ceiling submit = (timeout %d, clamped %v), want (60, false)",
			res.TimeoutSec, res.TimeoutClamped)
	}
	drainJobs(t, s)
}

// TestSubmitRunsWithConfiguredCeiling: the reported deadline is the one the job
// actually RUNS under — a request far above a 1s project ceiling is killed at the
// ceiling, proving the clamp reaches execute's context and is not just a response field.
func TestSubmitRunsWithConfiguredCeiling(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	s.Reload(withProjectCeiling(s.config(), "self", 1))

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "5"}, Cwd: ".", TimeoutSec: 3600,
	})
	if final.Status != StatusTimeout {
		t.Fatalf("expected timeout at the 1s project ceiling, got %s (err=%s)", final.Status, final.Error)
	}
	if final.TimeoutSec != 1 || !final.TimeoutClamped {
		t.Errorf("final timeout triple = (%d, %d, %v), want effective 1, requested 3600, clamped true",
			final.TimeoutSec, final.RequestedTimeoutSec, final.TimeoutClamped)
	}
}

func TestSubmitCancel(t *testing.T) {
	s := newTestService(t, t.TempDir())
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "5"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// Give the process a moment to start running, then cancel.
	waitForStatus(t, s, res.ID, StatusRunning, 2*time.Second)
	running, _ := s.Get(res.ID)
	if running.RenderedCommand == "" {
		t.Fatalf("running Get snapshot missing RenderedCommand")
	}
	list, err := s.ListJobs(ListOpts{Status: StatusRunning})
	if err != nil {
		t.Fatalf("ListJobs(running): %v", err)
	}
	var found bool
	for _, j := range list {
		if j.ID == res.ID {
			found = true
			if j.RenderedCommand == "" {
				t.Fatalf("running ListJobs snapshot missing RenderedCommand")
			}
		}
	}
	if !found {
		t.Fatalf("running ListJobs did not include %s", res.ID)
	}
	if err := s.Cancel(res.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	final, _ := s.Wait(res.ID)
	if final.Status != StatusCancelled {
		t.Fatalf("expected cancelled, got %s", final.Status)
	}
}

func TestCancelCompletedIsNoOp(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done")
	}
	// Cancelling a terminal job is a deterministic no-op (nil error), and does
	// not change the recorded status.
	if err := s.Cancel(final.ID); err != nil {
		t.Fatalf("cancel of completed job should be no-op, got %v", err)
	}
	again, _ := s.Get(final.ID)
	if again.Status != StatusDone {
		t.Fatalf("status changed after no-op cancel: %s", again.Status)
	}
}

func TestCancelUnknownJob(t *testing.T) {
	s := newTestService(t, t.TempDir())
	if err := s.Cancel("does-not-exist"); err == nil {
		t.Fatalf("expected error for unknown job id")
	}
}

func TestExecSecurityGate(t *testing.T) {
	s := newTestService(t, t.TempDir())
	_, err := s.Submit(JobRequest{
		ProjectKey: "noexec", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if err == nil {
		t.Fatalf("expected exec to be rejected when allow_exec=false")
	}
	if !strings.Contains(err.Error(), "allow_exec") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateConfigDefaultsAndAgentArgsGate(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		Projects: map[string]config.ProjectConfig{
			"open": {
				HostPath: root,
			},
			"openexec": {
				HostPath:  root,
				AllowExec: true,
			},
			"locked": {
				HostPath:      root,
				AllowedAgents: []string{"codex"},
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex":  {Type: agent.TypeCLIAgent, Command: "go", Args: []string{"env"}},
			"claude": {Type: agent.TypeCLIAgent, Command: "go", Args: []string{"env"}},
		},
	}
	s := newTestService(t, root)

	if _, err := s.Validate(cfg, JobRequest{ProjectKey: "open", Agent: "codex", Runner: "local", Prompt: "hi"}, false); err != nil {
		t.Fatalf("empty allowed_agents should allow configured cli agent: %v", err)
	}
	if _, err := s.Validate(cfg, JobRequest{ProjectKey: "open", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}}, false); err == nil || !strings.Contains(err.Error(), "allow_exec=false") {
		t.Fatalf("exec should still be rejected by allow_exec=false, got %v", err)
	}
	if _, err := s.Validate(cfg, JobRequest{ProjectKey: "openexec", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}}, false); err != nil {
		t.Fatalf("exec should pass when allow_exec=true and allowed_agents is empty: %v", err)
	}
	if _, err := s.Validate(cfg, JobRequest{ProjectKey: "locked", Agent: "claude", Runner: "local", Prompt: "hi"}, false); err == nil {
		t.Fatalf("non-empty allowed_agents should still reject unlisted agent")
	}
	if _, err := s.Validate(cfg, JobRequest{ProjectKey: "open", Agent: "codex", Runner: "local", Prompt: "hi"}, false); err != nil {
		t.Fatalf("empty allowed_runners should allow local runner: %v", err)
	}
	if _, err := s.Validate(cfg, JobRequest{ProjectKey: "open", Agent: "codex", Runner: "worker-x", Prompt: "hi"}, false); err == nil {
		t.Fatalf("empty allowed_runners should reject non-local runner")
	}
	if _, err := s.Validate(cfg, JobRequest{ProjectKey: "openexec", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, AgentArgs: []string{"--x"}}, false); err == nil || !strings.Contains(err.Error(), "agent_args not allowed") {
		t.Fatalf("exec + agent_args should be rejected, got %v", err)
	}
}

func TestSubmitCLIAgentArgsFlowToRenderedCommand(t *testing.T) {
	root := t.TempDir()
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
			"codex": {Type: agent.TypeCLIAgent, Command: "go", Args: []string{"env"}},
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
		Prompt: "hi", AgentArgs: []string{"GOOS"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s (err=%s)", final.Status, final.Error)
	}
	var rc struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(final.RenderedCommand), &rc); err != nil {
		t.Fatalf("RenderedCommand not valid JSON: %v (%q)", err, final.RenderedCommand)
	}
	if rc.Command != "go" || len(rc.Args) != 2 || rc.Args[0] != "env" || rc.Args[1] != "GOOS" {
		t.Fatalf("rendered command = %+v, want go [env GOOS]", rc)
	}
	var gotReq JobRequest
	if err := json.Unmarshal([]byte(final.RequestJSON), &gotReq); err != nil {
		t.Fatalf("request_json not valid JSON: %v", err)
	}
	if len(gotReq.AgentArgs) != 1 || gotReq.AgentArgs[0] != "GOOS" {
		t.Fatalf("request_json agent_args = %#v, want [GOOS]", gotReq.AgentArgs)
	}
}

func TestUnknownProjectRejected(t *testing.T) {
	s := newTestService(t, t.TempDir())
	if _, err := s.Submit(JobRequest{ProjectKey: "ghost", Agent: "exec", Runner: "local", Cmd: []string{"go"}}); err == nil {
		t.Fatalf("expected unknown project error")
	}
}

func TestAgentNotAllowedRejected(t *testing.T) {
	s := newTestService(t, t.TempDir())
	if _, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "hi"}); err == nil {
		t.Fatalf("expected agent-not-allowed error")
	}
}

func TestRunnerNotAllowedRejected(t *testing.T) {
	s := newTestService(t, t.TempDir())
	if _, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "docker-peer", Cmd: []string{"go"}}); err == nil {
		t.Fatalf("expected runner-not-allowed error")
	}
}

// TestTerminalMetadataPersistedToDB asserts the terminal job snapshot is
// persisted into the metadata store (the result.json file write was removed in
// SP2) and that the original request rides into the request_json column (SP5:
// the on-disk request.json file is no longer written).
func TestTerminalMetadataPersistedToDB(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	dir := filepath.Join(root, "self", final.ID)

	// Terminal metadata is queryable from the DB (not a result.json file).
	rec, ok, err := s.meta.GetJob(final.ID)
	if err != nil {
		t.Fatalf("meta.GetJob: %v", err)
	}
	if !ok {
		t.Fatalf("job %q not persisted to metadata store", final.ID)
	}
	if rec.ID != final.ID || rec.Status != StatusDone || rec.ExitCode != 0 {
		t.Fatalf("metadata record mismatch: %+v", rec)
	}
	if rec.ResultDir != final.ResultDir {
		t.Fatalf("metadata result_dir mismatch: %q != %q", rec.ResultDir, final.ResultDir)
	}
	if rec.UpdatedAt == 0 {
		t.Fatalf("metadata updated_at not stamped: %+v", rec)
	}
	// The original request is persisted into the request_json column (SP5), and
	// decodes back to the submitted request.
	if rec.RequestJSON == "" {
		t.Fatalf("metadata request_json not persisted: %+v", rec)
	}
	var gotReq JobRequest
	if err := json.Unmarshal([]byte(rec.RequestJSON), &gotReq); err != nil {
		t.Fatalf("request_json not valid JSON: %v", err)
	}
	if gotReq.ProjectKey != "self" || gotReq.Agent != "exec" || gotReq.Runner != "local" {
		t.Fatalf("request_json round-trip mismatch: %+v", gotReq)
	}
	// The on-disk request.json file must no longer be written (SP5).
	if _, err := os.Stat(filepath.Join(dir, "request.json")); !os.IsNotExist(err) {
		t.Fatalf("request.json should not be written on disk anymore, stat err=%v", err)
	}
}

// TestTitleRoundTripsThroughDB proves a submitted Title survives the DB read path
// (fromRecord parsing request_json), not just the live in-memory entry. The job is
// driven to terminal so its in-memory entry is evicted (SP3); Get/ListJobs then go
// through fromRecord, which must recover the title out of the stored request_json.
func TestTitleRoundTripsThroughDB(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	const title = "nightly cache warm"
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Title: title, Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done, got %s", final.Status)
	}
	// The job must be evicted from memory so the next reads exercise fromRecord
	// (the DB read path), not the live in-memory snapshot.
	if e := s.entry(final.ID); e != nil {
		t.Fatalf("setup: expected job evicted after terminal")
	}

	got, ok := s.Get(final.ID)
	if !ok {
		t.Fatalf("Get after eviction: job not found")
	}
	if got.Title != title {
		t.Fatalf("Get: title did not round-trip through DB read path, got %q want %q", got.Title, title)
	}

	list, err := s.ListJobs(ListOpts{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	var found bool
	for _, j := range list {
		if j.ID == final.ID {
			found = true
			if j.Title != title {
				t.Fatalf("ListJobs: title did not round-trip, got %q want %q", j.Title, title)
			}
		}
	}
	if !found {
		t.Fatalf("ListJobs after eviction does not contain %s", final.ID)
	}
}

func TestDefaultTitleFromCommandRoundTripsThroughDB(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done, got %s", final.Status)
	}
	const want = "go version"
	got, ok := s.Get(final.ID)
	if !ok {
		t.Fatalf("Get after terminal: job not found")
	}
	if got.Title != want {
		t.Fatalf("default title = %q, want %q", got.Title, want)
	}
	var gotReq JobRequest
	if err := json.Unmarshal([]byte(got.RequestJSON), &gotReq); err != nil {
		t.Fatalf("request_json not valid JSON: %v", err)
	}
	if gotReq.Title != want {
		t.Fatalf("request_json title = %q, want %q", gotReq.Title, want)
	}
}

func TestDefaultJobTitlePrefersCommandThenPrompt(t *testing.T) {
	cases := []struct {
		name string
		req  JobRequest
		want string
	}{
		{
			name: "command",
			req:  JobRequest{Cmd: []string{"go", "test", "./internal/job"}, Prompt: "prompt ignored"},
			want: "go test ./internal/job",
		},
		{
			name: "prompt runes",
			req:  JobRequest{Prompt: "请打开一个会话并查看状态"},
			want: "请打开一个会话并查看状态",
		},
		{
			name: "trim",
			req:  JobRequest{Prompt: "  hello world from prompt  "},
			want: "hello world from prompt",
		},
		{
			name: "cap at 32 runes",
			req:  JobRequest{Prompt: "1234567890123456789012345678901234567890"},
			want: "12345678901234567890123456789012",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultJobTitle(tc.req); got != tc.want {
				t.Fatalf("defaultJobTitle = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJobIDUniquenessSameSecond(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	// Pin the clock to a single second so ids only differ by the random suffix;
	// this is the cross-restart collision case the plan calls out.
	fixed := time.Date(2026, 6, 16, 1, 2, 3, 0, time.UTC)
	s.nowFn = func() time.Time { return fixed }

	const n = 200
	seen := map[string]bool{}
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		res, err := s.Submit(JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"sh", "-c", "exit 0"}, Cwd: ".", TimeoutSec: 30,
		})
		if err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
		if seen[res.ID] {
			t.Fatalf("duplicate job id: %s", res.ID)
		}
		seen[res.ID] = true
		ids = append(ids, res.ID)
		// Each id's dir must have been created.
		if _, err := os.Stat(res.ResultDir); err != nil {
			t.Fatalf("job dir not created for %s: %v", res.ID, err)
		}
	}
	if len(seen) != n {
		t.Fatalf("expected %d unique ids, got %d", n, len(seen))
	}
	// Drain all background jobs so their goroutines stop writing into root
	// before t.TempDir() cleanup runs (avoids a RemoveAll-vs-write race).
	for _, id := range ids {
		s.Wait(id)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	// Limit project "self" to 1 concurrent job.
	p := s.config().Projects["self"]
	p.MaxConcurrentJobs = 1
	s.config().Projects["self"] = p

	// Submit job1 (sleep) and wait until it is actually running and holding the
	// single slot BEFORE submitting job2, so the slot ownership is deterministic.
	r1, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"sleep", "1"}, Cwd: ".", TimeoutSec: 30})
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, s, r1.ID, StatusRunning, 2*time.Second)
	r2, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"sh", "-c", "exit 0"}, Cwd: ".", TimeoutSec: 30})
	if err != nil {
		t.Fatal(err)
	}
	// Give job2's goroutine a moment to reach the (blocked) slot acquisition.
	time.Sleep(50 * time.Millisecond)
	// While job1 runs, job2 must still be queued (slot held by job1).
	if j2, _ := s.Get(r2.ID); j2.Status != StatusQueued {
		t.Fatalf("expected job2 queued while job1 runs, got %s", j2.Status)
	}
	// Both eventually complete.
	f1, _ := s.Wait(r1.ID)
	f2, _ := s.Wait(r2.ID)
	if f1.Status != StatusDone || f2.Status != StatusDone {
		t.Fatalf("expected both done, got %s/%s", f1.Status, f2.Status)
	}
}

// waitForStatus polls until the job reaches want or the deadline elapses.
func waitForStatus(t *testing.T, s *Service, id, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if r, ok := s.Get(id); ok && r.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r, _ := s.Get(id)
	t.Fatalf("job %s did not reach %q in time (status=%s)", id, want, r.Status)
}

// drainOnClose registers the teardown drain for a service a test just built and
// returns it, so a constructor reads `return drainOnClose(t, NewService(...))`.
//
// Every test constructor should use it: a test that only asserts on submission-time
// state leaves its job — or a fallback / auto-resume / verify continuation it
// spawned — still writing into the test's TempDir, and the framework's RemoveAll
// then fails with "directory not empty" (or "file in use" on Windows). Register it
// where the store's own Close cleanup is registered: cleanups are LIFO, so the drain
// runs BEFORE the store closes and before the TempDir is removed.
func drainOnClose(t *testing.T, s *Service) *Service {
	t.Helper()
	t.Cleanup(func() { drainJobs(t, s) })
	return s
}

// drainJobs ends the jobs a test left in flight and waits (bounded) for them to reach
// a terminal state. A test that only inspects the Submit result (or a subtest that
// asserts on a snapshot) would otherwise return while its job — or a fallback /
// auto-resume / verify continuation it spawned — is still writing into the test's
// TempDir, and the framework's RemoveAll then fails with "directory not empty" (or
// "file in use" on Windows). Cancelling is safe here: the test is over and its own
// cleanup, if any, already ran (cleanups are LIFO).
//
// It requires a QUIET WINDOW rather than a single-shot "nothing in flight" read: a
// job submitted concurrently with the previous observation (a racing finish-hook
// continuation, a fallback chain's next link) must be seen and cancelled, not slip
// past the check that then returns. drainBudget bounds the whole wait: a job that
// ignores cancellation (a parked interactive/pty session, say) must not stall the
// whole suite — the wait only has to cover jobs that end promptly once cancelled.
const (
	drainBudget      = 2 * time.Second
	drainQuietRounds = 3
	drainRoundSleep  = 10 * time.Millisecond
)

func drainJobs(t *testing.T, s *Service) {
	t.Helper()
	deadline := time.Now().Add(drainBudget)
	seen := map[string]bool{}
	quiet := 0
	for time.Now().Before(deadline) {
		list, err := s.ListJobs(ListOpts{Limit: 500})
		if err != nil {
			return
		}
		moved, inFlight := false, 0
		for _, j := range list {
			if !seen[j.ID] {
				seen[j.ID] = true
				moved = true
			}
			if !isTerminal(j.Status) {
				inFlight++
				_ = s.Cancel(j.ID)
			}
		}
		if inFlight == 0 && !moved {
			if quiet++; quiet >= drainQuietRounds {
				return
			}
		} else {
			quiet = 0
		}
		time.Sleep(drainRoundSleep)
	}
}
