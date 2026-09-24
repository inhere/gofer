package job

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	ptyrunner "github.com/inhere/gofer/internal/runner/pty"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// SEC-01 acceptance: a job process — and the verify step it spawns — must not
// inherit the credentials of the gofer process that launched it, and must instead
// carry its own GOFER_JOB_TOKEN.

// credentialEnvNames are the variables these tests look for in a spawned child.
var credentialEnvNames = []string{"GOFER_TOKEN", "GOFER_SERVER_TOKEN", "GOFER_WORKER_TOKEN", "GOFER_JOB_TOKEN"}

// envPrintArgs is the argv that makes a child print the variables above.
func envPrintArgs() []string {
	return append([]string{"env-print"}, credentialEnvNames...)
}

// setServerCredentials puts the three credential variables into THIS process's
// environment, which is exactly how the leak happened: gofer's own serve inherits
// them from the deployment's .env and used to hand them straight to every job.
func setServerCredentials(t *testing.T) {
	t.Helper()
	t.Setenv("GOFER_TOKEN", "tok-server")
	t.Setenv("GOFER_SERVER_TOKEN", "tok-server-2")
	t.Setenv("GOFER_WORKER_TOKEN", "tok-worker")
}

// assertNoInheritedCredentials asserts on a child's env-print output: the three
// credential variables are EMPTY (env-print always prints the requested names, so an
// empty value is what "not inherited" looks like) and GOFER_JOB_TOKEN is set.
func assertNoInheritedCredentials(t *testing.T, what, out string) {
	t.Helper()
	values := parseEnvPrint(out)
	for _, name := range []string{"GOFER_TOKEN", "GOFER_SERVER_TOKEN", "GOFER_WORKER_TOKEN"} {
		if v := values[name]; v != "" {
			t.Fatalf("%s: %s leaked into the job child (value %q)\n%s", what, name, v, out)
		}
	}
	if _, ok := values["GOFER_JOB_TOKEN"]; !ok {
		t.Fatalf("%s: the child printed no GOFER_JOB_TOKEN line\n%s", what, out)
	}
	if values["GOFER_JOB_TOKEN"] == "" {
		t.Fatalf("%s: the job child has no GOFER_JOB_TOKEN\n%s", what, out)
	}
}

// parseEnvPrint reads env-print output into a name→value map (a name with no "=" or
// an empty name is not an env line and is skipped).
func parseEnvPrint(out string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		name, value, ok := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if !ok || name == "" || strings.ContainsAny(name, " \t") {
			continue
		}
		values[name] = value
	}
	return values
}

// TestJobEnvHasNoServerToken is the SEC-01 acceptance across all four places a job
// process is spawned: the local runner, the pty runner, the acp client and the verify
// step. Each must drop the inherited credentials and keep the job's own token.
func TestJobEnvHasNoServerToken(t *testing.T) {
	setServerCredentials(t)

	t.Run("local_and_verify", func(t *testing.T) {
		root := t.TempDir()
		s := newTestService(t, root)
		final := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: testcmd.Cmd(t, envPrintArgs()...),
			Cwd: ".",
			// The verify step is a SECOND child of the gofer process, and it used to
			// build its own environment from os.Environ() — so it is asserted
			// separately from the agent's run.
			Verify:     testcmd.Cmd(t, envPrintArgs()...),
			TimeoutSec: 60,
		})
		if final.Status != StatusDone {
			t.Fatalf("job status = %s (err=%s)", final.Status, final.Error)
		}
		stdout := readJobLog(t, final, store.StdoutFile)
		assertNoInheritedCredentials(t, "agent run", stdout)
		// The verify step's output lands in the job's stderr log (SUP-01 B merges both
		// of the step's streams there).
		stderr := readJobLog(t, final, store.StderrFile)
		assertNoInheritedCredentials(t, "verify step", stderr)
		// The verify step ran with the SAME credential the agent did: a second, freshly
		// minted token for one job would be one nobody revokes.
		agentToken := parseEnvPrint(stdout)["GOFER_JOB_TOKEN"]
		if verifyToken := parseEnvPrint(stderr)["GOFER_JOB_TOKEN"]; verifyToken != agentToken {
			t.Fatalf("verify step ran with a different credential: %q vs agent %q", verifyToken, agentToken)
		}
	})

	t.Run("pty", func(t *testing.T) {
		// The pty child is the second spawn site on this machine. It is driven through
		// the RUNNER (an interactive JOB would additionally need an agent with
		// interactive argv, which proves nothing about the line under test: the
		// util.EnvironWithout call in pty.start).
		out := &syncBuffer{}
		pr := ptyrunner.New()
		pr.SetObserver(sessionCopier{dst: out})
		res := pr.Run(context.Background(), runner.Request{
			JobID:   "pty-env-1",
			Command: testcmd.Path(t),
			Args:    envPrintArgs(),
			WorkDir: t.TempDir(),
			Env:     map[string]string{EnvJobToken: "gjt_pty_test"},
			EnvDeny: DefaultJobEnvDeny,
		})
		if res.ExitCode != 0 {
			t.Fatalf("pty run exit=%d err=%v", res.ExitCode, res.Err)
		}
		// A ConPTY child's stream carries escape sequences (a title-change OSC lands
		// between the lines), so it cannot be read back with parseEnvPrint — the escape
		// prefix renames the first key and every lookup answers "empty". The leaked
		// VALUES are what is searched for instead.
		stream := waitForEnvPrint(t, out)
		for _, leaked := range []string{"tok-server", "tok-server-2", "tok-worker"} {
			if strings.Contains(stream, leaked) {
				t.Fatalf("pty run: credential value %q reached the job child\n%q", leaked, stream)
			}
		}
		if !strings.Contains(stream, "GOFER_JOB_TOKEN="+JobTokenPrefix) {
			t.Fatalf("the pty child lost its own credential\n%q", stream)
		}
	})

	t.Run("acp", func(t *testing.T) {
		// Same reasoning: acp.Start is the spawn site the acp RUNNER goes through, and
		// it is driven with the deny list the job service resolves. The fake-agent
		// protocol never starts — the question is what the child process was launched
		// with.
		stderr := &syncBuffer{}
		client, err := acp.Start(context.Background(), acp.Options{
			Command: testcmd.Path(t),
			Args:    append([]string{"env-print-err"}, credentialEnvNames...),
			Dir:     t.TempDir(),
			Env:     map[string]string{EnvJobToken: "gjt_acp_test"},
			EnvDeny: DefaultJobEnvDeny,
			Stderr:  stderr,
		})
		if err != nil {
			t.Fatalf("acp.Start: %v", err)
		}
		defer func() { _ = client.Close() }()
		assertNoInheritedCredentials(t, "acp run", waitForEnvPrint(t, stderr))
	})
}

// assertConfigDirDropped asserts on a child's env-print output that the server's
// config dir did not reach it. env-print always prints the requested names, and the
// test process carries a non-empty GOFER_CONFIG_DIR, so "empty" is what "dropped"
// looks like.
func assertConfigDirDropped(t *testing.T, what, out string) {
	t.Helper()
	if v := parseEnvPrint(out)[config.EnvConfigDir]; v != "" {
		t.Fatalf("%s: %s leaked into the job child (value %q)\n%s", what, config.EnvConfigDir, v, out)
	}
}

// TestJobEnvDropsConfigDir is F10's fix for the first leak of the v0.60 field trial: a
// job inherited GOFER_CONFIG_DIR, and the `gofer` the HOST happened to have on PATH
// then auto-loaded <config-dir>/.env — the SERVER's file, which holds the operator's
// GOFER_TOKEN — and called the API as the user (that CLI was 0.53.1 and knew nothing
// about the job's own credential, so it never even looked at GOFER_JOB_TOKEN).
//
// The variable is a pointer to credentials, so it is denied by default on every path a
// job child is spawned: the local runner, the pty runner, the acp client and the verify
// step. A project can still re-admit it deliberately (job_env_allow), which is what the
// last subtest pins.
func TestJobEnvDropsConfigDir(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	args := append([]string{"env-print"}, config.EnvConfigDir, "GOFER_JOB_TOKEN")

	t.Run("local_and_verify", func(t *testing.T) {
		root := t.TempDir()
		s := newTestService(t, root)
		final := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd:    testcmd.Cmd(t, args...),
			Cwd:    ".",
			Verify: testcmd.Cmd(t, args...),
			// The verify step is a second child of the gofer process and builds its own
			// environment, so it is asserted on its own.
			TimeoutSec: 60,
		})
		if final.Status != StatusDone {
			t.Fatalf("job status = %s (err=%s)", final.Status, final.Error)
		}
		assertConfigDirDropped(t, "agent run", readJobLog(t, final, store.StdoutFile))
		assertConfigDirDropped(t, "verify step", readJobLog(t, final, store.StderrFile))
	})

	t.Run("pty", func(t *testing.T) {
		// A short config-dir value on purpose: a ConPTY stream is hard-wrapped at the
		// console width, and the value has to stay searchable in the raw stream (see the
		// pty case of TestJobEnvHasNoServerToken for why parseEnvPrint cannot be used).
		shortDir := "C:/f10-pty-cfgdir"
		t.Setenv(config.EnvConfigDir, shortDir)
		out := &syncBuffer{}
		pr := ptyrunner.New()
		pr.SetObserver(sessionCopier{dst: out})
		res := pr.Run(context.Background(), runner.Request{
			JobID:   "pty-cfgdir-1",
			Command: testcmd.Path(t),
			Args:    args,
			WorkDir: t.TempDir(),
			Env:     map[string]string{EnvJobToken: "gjt_pty_test"},
			EnvDeny: DefaultJobEnvDeny,
		})
		if res.ExitCode != 0 {
			t.Fatalf("pty run exit=%d err=%v", res.ExitCode, res.Err)
		}
		stream := waitForEnvPrint(t, out)
		if !strings.Contains(stream, config.EnvConfigDir+"=") {
			t.Fatalf("the pty child printed no %s line\n%q", config.EnvConfigDir, stream)
		}
		if strings.Contains(stream, config.EnvConfigDir+"="+shortDir) {
			t.Fatalf("pty run: %s leaked into the job child (value %q)\n%q", config.EnvConfigDir, shortDir, stream)
		}
	})

	t.Run("acp", func(t *testing.T) {
		stderr := &syncBuffer{}
		client, err := acp.Start(context.Background(), acp.Options{
			Command: testcmd.Path(t),
			Args:    append([]string{"env-print-err"}, config.EnvConfigDir, "GOFER_JOB_TOKEN"),
			Dir:     t.TempDir(),
			Env:     map[string]string{EnvJobToken: "gjt_acp_test"},
			EnvDeny: DefaultJobEnvDeny,
			Stderr:  stderr,
		})
		if err != nil {
			t.Fatalf("acp.Start: %v", err)
		}
		defer func() { _ = client.Close() }()
		assertConfigDirDropped(t, "acp run", waitForEnvPrint(t, stderr))
	})

	t.Run("job_env_allow", func(t *testing.T) {
		root := t.TempDir()
		cfg := &config.Config{
			Storage: config.StorageConfig{Root: root},
			Projects: map[string]config.ProjectConfig{
				"self": {
					HostPath: root, AllowedAgents: []string{"exec"}, AllowedRunners: []string{"local"},
					AllowExec: true, JobEnvAllow: []string{config.EnvConfigDir},
				},
			},
		}
		meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
		if err != nil {
			t.Fatalf("open jobstore: %v", err)
		}
		t.Cleanup(func() { _ = meta.Close() })
		s := drainOnClose(t, NewService(cfg, project.NewRegistry(cfg, ""), agent.NewRegistry(cfg),
			map[string]runner.Runner{localrunner.Name: localrunner.New()}, meta, nil))

		allowed := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: testcmd.Cmd(t, args...), Cwd: ".", TimeoutSec: 60,
		})
		if allowed.Status != StatusDone {
			t.Fatalf("allowed job status = %s (err=%s)", allowed.Status, allowed.Error)
		}
		if got := parseEnvPrint(readJobLog(t, allowed, store.StdoutFile))[config.EnvConfigDir]; got != cfgDir {
			t.Fatalf("job_env_allow did not re-admit %s: %q", config.EnvConfigDir, got)
		}
		assertEventRecorded(t, meta, allowed.ID, EventJobEnvAllowed, config.EnvConfigDir)
	})
}

// TestJobPathPrefersServingBinary is F10's fix for the second leak of the v0.60 field
// trial: the `gofer` a job found on PATH was whatever the HOST had installed (0.53.1 on
// a 0.60.0 server), so an agent following a leader prompt either lacked the subcommand
// it was told to run or used a CLI that predates job credentials and authenticated as
// the operator. A job's credential only means anything when the binary reading it is the
// one that issued it, so the directory of the gofer executable RUNNING the job goes
// first on PATH, and GOFER_BIN names that file outright.
func TestJobPathPrefersServingBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	inherited := os.Getenv("PATH")

	root := t.TempDir()
	s := newTestService(t, root)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "env-print", "PATH", "GOFER_BIN"), Cwd: ".", TimeoutSec: 60,
	})
	if final.Status != StatusDone {
		t.Fatalf("job status = %s (err=%s)", final.Status, final.Error)
	}
	values := parseEnvPrint(readJobLog(t, final, store.StdoutFile))

	if got := values["GOFER_BIN"]; got != exe {
		t.Fatalf("GOFER_BIN = %q, want the running gofer executable %q", got, exe)
	}
	// PATH is the gofer dir + the separator the platform uses + the inherited PATH: the
	// job still finds everything it could before, just not `gofer` first.
	parts := strings.SplitN(values["PATH"], string(os.PathListSeparator), 2)
	if want := filepath.Dir(exe); parts[0] != want {
		t.Fatalf("job PATH starts with %q, want the gofer binary's directory %q\n%s", parts[0], want, values["PATH"])
	}
	if len(parts) != 2 || parts[1] != inherited {
		t.Fatalf("job PATH tail = %q, want the inherited PATH %q", parts[1], inherited)
	}
}

// TestJobEnvAllowlistPerProject: a project may name inherited variables it WANTS its
// jobs to keep (job_env_allow), that allowance is recorded on the job, and a project
// without the list is unaffected.
func TestJobEnvAllowlistPerProject(t *testing.T) {
	t.Setenv("GOFER_TOKEN", "tok-server")

	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath: root, AllowedAgents: []string{"exec"}, AllowedRunners: []string{"local"},
				AllowExec: true, JobEnvAllow: []string{"GOFER_TOKEN"},
			},
			"strict": {
				HostPath: root, AllowedAgents: []string{"exec"}, AllowedRunners: []string{"local"},
				AllowExec: true,
			},
		},
	}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	s := drainOnClose(t, NewService(cfg, project.NewRegistry(cfg, ""), agent.NewRegistry(cfg),
		map[string]runner.Runner{localrunner.Name: localrunner.New()}, meta, nil))

	allowed := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, envPrintArgs()...), Cwd: ".", TimeoutSec: 60,
	})
	if allowed.Status != StatusDone {
		t.Fatalf("allowed job status = %s (err=%s)", allowed.Status, allowed.Error)
	}
	stdout := readJobLog(t, allowed, store.StdoutFile)
	values := parseEnvPrint(stdout)
	if values["GOFER_TOKEN"] != "tok-server" {
		t.Fatalf("job_env_allow did not re-admit GOFER_TOKEN: %q\n%s", values["GOFER_TOKEN"], stdout)
	}
	if values["GOFER_JOB_TOKEN"] == "" {
		t.Fatalf("the allowed job lost its own credential")
	}
	assertEventRecorded(t, meta, allowed.ID, EventJobEnvAllowed, "GOFER_TOKEN")

	// The other project inherits nothing, and records no allowance: an event for a list
	// that fired on nothing would be noise on every job of the project.
	strict := submitAndWait(t, s, JobRequest{
		ProjectKey: "strict", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, envPrintArgs()...), Cwd: ".", TimeoutSec: 60,
	})
	if strict.Status != StatusDone {
		t.Fatalf("strict job status = %s (err=%s)", strict.Status, strict.Error)
	}
	assertNoInheritedCredentials(t, "strict project", readJobLog(t, strict, store.StdoutFile))
	assertNoEvent(t, meta, strict.ID, EventJobEnvAllowed)
}

// TestJobTokenIssuedAndRevoked: the credential works while the job runs, dies with it,
// falls back to its own expiry when nothing revokes it, and the store keeps a hash.
func TestJobTokenIssuedAndRevoked(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, envPrintArgs()...), Cwd: ".", TimeoutSec: 60,
	})
	token := parseEnvPrint(readJobLog(t, final, store.StdoutFile))["GOFER_JOB_TOKEN"]
	if !strings.HasPrefix(token, JobTokenPrefix+final.ID+"_") {
		t.Fatalf("token %q is not gjt_<job_id>_<hex> for job %s", token, final.ID)
	}

	// A job that has reached its terminal state answers exactly like an unknown token.
	if _, ok := s.LookupJobToken(token); ok {
		t.Fatalf("the credential of a terminal job still authenticates")
	}

	rec, ok, err := s.Meta().GetJobToken(final.ID)
	if err != nil {
		t.Fatalf("GetJobToken: %v", err)
	}
	if !ok {
		t.Fatalf("no job_tokens row for job %s", final.ID)
	}
	if rec.TokenHash == token || rec.TokenHash != hashJobToken(token) {
		t.Fatalf("stored value %q is not sha256(token)", rec.TokenHash)
	}
	if rec.RevokedAt == 0 {
		t.Fatalf("the terminal path did not revoke the credential: %+v", rec)
	}
	if rec.Kind != jobstore.JobCredentialMember {
		t.Fatalf("kind = %q, want member", rec.Kind)
	}

	// Fallback expiry: a credential whose job never reached its terminal path (a
	// crashed hub) must stop working on its own. The row is written directly — that IS
	// the situation under test (nothing ever revoked it).
	stale, err := newJobToken("job-expired")
	if err != nil {
		t.Fatalf("newJobToken: %v", err)
	}
	if err := s.Meta().UpsertJobToken(jobstore.JobTokenRecord{
		JobID: "job-expired", TokenHash: hashJobToken(stale), Kind: jobstore.JobCredentialMember,
		ExpiresAt: time.Now().Add(-time.Minute).Unix(), CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("UpsertJobToken: %v", err)
	}
	if _, ok := s.LookupJobToken(stale); ok {
		t.Fatalf("an expired credential still authenticates")
	}
}

// assertEventRecorded proves the job's own event stream carries eventType and that its
// detail names wantDetail (the detail is JSON, so "the key is named" is the assertion).
func assertEventRecorded(t *testing.T, meta *jobstore.Store, jobID, eventType, wantDetail string) {
	t.Helper()
	events, err := meta.ListJobEvents(jobID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	for _, e := range events {
		if e.Type != eventType {
			continue
		}
		if !strings.Contains(e.Detail, wantDetail) {
			t.Fatalf("%s detail %q does not name %q", eventType, e.Detail, wantDetail)
		}
		return
	}
	t.Fatalf("no %s event on job %s (%d events)", eventType, jobID, len(events))
}

// assertNoEvent proves eventType was NOT recorded for the job.
func assertNoEvent(t *testing.T, meta *jobstore.Store, jobID, eventType string) {
	t.Helper()
	events, err := meta.ListJobEvents(jobID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	for _, e := range events {
		if e.Type == eventType {
			t.Fatalf("unexpected %s event on job %s: %s", eventType, jobID, e.Detail)
		}
	}
}

// syncBuffer is a concurrency-safe bytes.Buffer for the sinks the runners write from
// their own goroutines (the pty pump, the acp process's stderr).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// sessionCopier is the pty observer that drains a session into dst — the standalone
// reader a pty-less caller uses (the observer owns the sole read side of the pty).
type sessionCopier struct{ dst io.Writer }

func (s sessionCopier) OnSessionStart(_ string, sess *ptyrunner.PtySession) {
	go func() { _, _ = io.Copy(s.dst, sess) }()
}

// waitForEnvPrint waits (bounded) for the child's output to arrive on a side channel
// and returns it. The pty and acp cases read a stream rather than a job log, so there
// is no terminal state to wait on; polling for the print is the honest signal.
func waitForEnvPrint(t *testing.T, out *syncBuffer) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got := out.String()
		if strings.Contains(got, "GOFER_JOB_TOKEN=") {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the child printed no env output within the deadline: %q", out.String())
	return ""
}
