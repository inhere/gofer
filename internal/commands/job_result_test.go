package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// newJobResultTestServer wires a REAL in-process httpapi server plus its store, so a
// `job result` test asserts the server's own answer — the record written on the job, the
// 403 of another job's credential, the 409 of a second report — instead of a stub's echo.
// One user caller is configured because an empty caller set admits every request as a
// user, which would bypass the job-credential path under test.
func newJobResultTestServer(t *testing.T) (string, *jobstore.Store) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
		},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"exec"}, AllowedRunners: []string{"local"}, AllowExec: true},
		},
	}
	st, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, st, nil)
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)

	ts := httptest.NewServer(httpapi.New(
		&cfg.Server, "", false, jobs, eng, projects, agents, nil, nil, nil, nil,
	).Handler())
	t.Cleanup(ts.Close)
	return ts.URL, st
}

// seedJobWithCredential stores a running job and returns its job credential — the pair
// every job's own process carries (GOFER_JOB_ID + GOFER_JOB_TOKEN).
func seedJobWithCredential(t *testing.T, st *jobstore.Store, id string) string {
	t.Helper()
	now := time.Now().Unix()
	if err := st.UpsertJob(jobstore.JobRecord{
		ID: id, ProjectKey: "self", Agent: "exec", Runner: "local", Status: job.StatusRunning,
		Cwd: ".", StartedAt: now, UpdatedAt: now, CallerID: "alice", OriginCaller: "alice",
	}); err != nil {
		t.Fatalf("seed job %s: %v", id, err)
	}
	token := "gjt_" + id + "_" + strings.Repeat("0123456789abcdef", 2)
	sum := sha256.Sum256([]byte(token))
	if err := st.UpsertJobToken(jobstore.JobTokenRecord{
		JobID: id, TokenHash: hex.EncodeToString(sum[:]), Kind: jobstore.JobCredentialMember,
		ExpiresAt: now + 3600, CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed job token %s: %v", id, err)
	}
	return token
}

// jobResultCLI runs one `job result …` through the real command dispatch and returns its
// exit code and output. The flag structs are package globals gcli never resets, so every
// run starts from the pristine snapshot (see flagreset_test.go).
func jobResultCLI(t *testing.T, args ...string) (string, int) {
	t.Helper()
	resetFlagGlobals()
	app := NewApp("test")
	var code int
	out := captureOutput(t, func() { code = app.Run(args) })
	return out, code
}

// TestJobResultCommand (gofer-4qsq): the CLI face of gofer_submit_result. A job reports
// with its OWN credential, defaulting the id to $GOFER_JOB_ID; the report lands on the
// job; a missing / invalid decision never reaches the server; another job's id and a
// second report come back as the server's own readable refusals.
func TestJobResultCommand(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })

	server, st := newJobResultTestServer(t)
	tokenA := seedJobWithCredential(t, st, "job-res-a")
	tokenB := seedJobWithCredential(t, st, "job-res-b")
	t.Setenv(jobIDEnv, "job-res-a")
	t.Setenv(job.EnvJobToken, tokenA)

	// A missing or invalid decision is refused BEFORE any request: an agent typo must not
	// look like a server problem.
	if out, code := jobResultCLI(t, "job", "result", "--reason", "x", "--server", server); code == 0 || !strings.Contains(out, "--decision") {
		t.Fatalf("missing --decision must fail with a hint (exit %d):\n%s", code, out)
	}
	if out, code := jobResultCLI(t, "job", "result", "--decision", "maybe", "--reason", "x", "--server", server); code == 0 || !strings.Contains(out, "pass, rework, fail, human") {
		t.Fatalf("an invalid decision must be refused (exit %d):\n%s", code, out)
	}
	if out, code := jobResultCLI(t, "job", "result", "--decision", "pass", "--server", server); code == 0 || !strings.Contains(out, "--reason") {
		t.Fatalf("a missing --reason must fail with a hint (exit %d):\n%s", code, out)
	}
	if got, _, _ := st.GetJob("job-res-a"); got.ResultDecision != "" {
		t.Fatalf("a refused report wrote %q on the job", got.ResultDecision)
	}

	// The id comes from $GOFER_JOB_ID, the credential from $GOFER_JOB_TOKEN: no flags, no
	// secret on the command line.
	out, code := jobResultCLI(t, "job", "result", "--decision", "human", "--reason", "两处接口取舍要人拍板",
		"--action-required", "选 A 还是 B", "--finding", "A 改动小但慢", "--server", server)
	if code != 0 {
		t.Fatalf("job result exit code=%d:\n%s", code, out)
	}
	if !strings.Contains(out, "reported:   human — 两处接口取舍要人拍板") || !strings.Contains(out, "action required: 选 A 还是 B") ||
		!strings.Contains(out, "- A 改动小但慢") {
		t.Fatalf("output must name the recorded outcome:\n%s", out)
	}
	rec, ok, err := st.GetJob("job-res-a")
	if err != nil || !ok {
		t.Fatalf("GetJob(job-res-a): ok=%v err=%v", ok, err)
	}
	if rec.ResultDecision != job.ResultHuman || rec.ResultReason != "两处接口取舍要人拍板" || !strings.Contains(rec.ResultReportJSON, "选 A 还是 B") {
		t.Fatalf("the report did not land on the job: %+v", rec)
	}

	// A second report for the same job: the server's 409 text, readably, and the first
	// report stays.
	if out, code := jobResultCLI(t, "job", "result", "--decision", "pass", "--reason", "again", "--server", server); code == 0 ||
		!strings.Contains(out, "409") || !strings.Contains(out, "already reported") {
		t.Fatalf("a second report must fail with the 409 text (exit %d):\n%s", code, out)
	}
	if rec, _, _ := st.GetJob("job-res-a"); rec.ResultDecision != job.ResultHuman {
		t.Fatalf("the second report overwrote the first: %+v", rec)
	}

	// Another job's id with this credential: 403, never a record (an explicit <job-id> is
	// supported, but it does not widen the credential).
	if out, code := jobResultCLI(t, "job", "result", "job-res-b", "--decision", "pass", "--reason", "x", "--server", server); code == 0 ||
		!strings.Contains(out, "403") {
		t.Fatalf("another job's id must be refused (exit %d):\n%s", code, out)
	}
	if rec, _, _ := st.GetJob("job-res-b"); rec.ResultDecision != "" {
		t.Fatalf("job B got a report it never made: %+v", rec)
	}

	// Outside a job (no $GOFER_JOB_ID) the id argument is required and the error says why.
	t.Setenv(jobIDEnv, "")
	t.Setenv(job.EnvJobToken, tokenB)
	if out, code := jobResultCLI(t, "job", "result", "--decision", "pass", "--reason", "x", "--server", server); code == 0 ||
		!strings.Contains(out, jobIDEnv) {
		t.Fatalf("a missing id outside a job must be refused and name %s (exit %d):\n%s", jobIDEnv, code, out)
	}
	// With the id given explicitly the same credential reports its own job.
	if out, code := jobResultCLI(t, "job", "result", "job-res-b", "--decision", "rework", "--reason", "还差测试", "--server", server); code != 0 {
		t.Fatalf("explicit id exit code=%d:\n%s", code, out)
	}
	if rec, _, _ := st.GetJob("job-res-b"); rec.ResultDecision != job.ResultRework || rec.ResultReason != "还差测试" {
		t.Fatalf("explicit id report = %+v", rec)
	}
}
