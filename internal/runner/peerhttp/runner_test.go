package peerhttp_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/runner/peerhttp"
	"github.com/inhere/gofer/internal/store"
)

// bridge bundles a wired job.Service + httpapi.Server for one node (host/peer).
type bridge struct {
	jobs *job.Service
	srv  *httptest.Server
}

// openTestStore opens a metadata store under root (cleaned up automatically) for
// wiring a job.Service in the peer/host bridge fixtures.
func openTestStore(t *testing.T, root string) *jobstore.Store {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func (b *bridge) close() { b.srv.Close() }

// newPeerBridge starts a "peer" bridge: project "demo" allows the exec agent
// with allow_exec=true and the built-in local runner; auth uses an empty token.
func newPeerBridge(t *testing.T) *bridge {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{AllowEmptyToken: true},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"demo": {
				HostPath:       root, // cwd "." resolves under here
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil)
	s := httpapi.New(&cfg.Server, "", true, jobs, projects, agents, nil, nil, nil, nil)
	return &bridge{jobs: jobs, srv: httptest.NewServer(s.Handler())}
}

// newHostBridge starts a "host" bridge whose project "demo" routes jobs through
// a peer-http runner "docker-peer" pointed at peerURL. The host has no local
// agent config beyond the built-in exec; the peer resolves/executes the job.
func newHostBridge(t *testing.T, peerURL string) *bridge {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{AllowEmptyToken: true},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"demo": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"docker-peer"},
				// allow_exec is intentionally false: the host must NOT impose its
				// exec gate on remote jobs (the peer enforces its own).
			},
		},
		Runners: map[string]config.RunnerConfig{
			"docker-peer": {Type: "peer-http", BaseURL: peerURL},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{
		localrunner.Name: localrunner.New(),
		"docker-peer":    peerhttp.New("docker-peer", peerURL, ""),
	}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil)
	s := httpapi.New(&cfg.Server, "", true, jobs, projects, agents, nil, nil, nil, nil)
	return &bridge{jobs: jobs, srv: httptest.NewServer(s.Handler())}
}

// waitTerminal polls the in-process job service until the job is terminal.
func waitTerminal(t *testing.T, b *bridge, id string, timeout time.Duration) job.JobResult {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		jr, ok := b.jobs.Get(id)
		if ok && job.IsTerminal(jr.Status) {
			return jr
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach terminal state within %s", id, timeout)
	return job.JobResult{}
}

// waitRunning polls until the job reaches running (or terminal) so cancel has a
// live process to target.
func waitRunning(t *testing.T, b *bridge, id string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		jr, ok := b.jobs.Get(id)
		if ok && (jr.Status == job.StatusRunning || job.IsTerminal(jr.Status)) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach running within %s", id, timeout)
}

// TestPeerRunnerForwardsAndMirrorsLogs submits an exec job to the HOST bridge
// with runner=docker-peer; the host forwards it to the PEER, which executes it,
// and the host job ends done/exit-0 with the peer's stdout MIRRORED into the
// host's local log.
func TestPeerRunnerForwardsAndMirrorsLogs(t *testing.T) {
	peer := newPeerBridge(t)
	defer peer.close()
	host := newHostBridge(t, peer.srv.URL)
	defer host.close()

	created, err := host.jobs.Submit(job.JobRequest{
		ProjectKey: "demo",
		Agent:      "exec",
		Runner:     "docker-peer",
		Cmd:        []string{"sh", "-c", "echo line1 && echo line2"},
		Cwd:        ".",
		TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("host submit: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("host job has no id")
	}

	final := waitTerminal(t, host, created.ID, 15*time.Second)
	if final.Status != job.StatusDone {
		t.Fatalf("host job status=%q exit=%d err=%q, want done/0", final.Status, final.ExitCode, final.Error)
	}
	if final.ExitCode != 0 {
		t.Fatalf("host job exit=%d, want 0", final.ExitCode)
	}

	// Log mirroring: the peer's stdout must appear in the HOST's local stdout.log,
	// read back via the host's own /logs path (store.ReadLogTail).
	out := readHostStdout(t, host, created.ID)
	for _, want := range []string{"line1", "line2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("host stdout.log missing mirrored %q; got %q", want, out)
		}
	}

	// The peer must also have executed exactly one job that finished done.
	peerJobs, _ := peer.jobs.ListJobs(job.ListOpts{})
	if len(peerJobs) == 0 {
		t.Fatalf("peer ran no job")
	}
	var peerDone bool
	for _, pj := range peerJobs {
		if pj.Status == job.StatusDone {
			peerDone = true
		}
	}
	if !peerDone {
		t.Fatalf("peer job not done: %+v", peerJobs)
	}
}

// TestPeerRunnerCapturesOutcome (P4-b full stack): a peer-executed job writes a
// result.json + an artifact + changes a tracked git file. The peer's own
// captureOutcomes records rendered_command / result_json / diff_summary /
// artifacts清单; the host peer-http runner回传 them (get_job for the small fields,
// the /artifacts endpoint for the manifest) and stamps source=peer:<name>. The
// host job's snapshot + persisted row must then carry all of them.
func TestPeerRunnerCapturesOutcome(t *testing.T) {
	peer := newPeerBridgeGit(t)
	defer peer.close()
	host := newHostBridge(t, peer.srv.URL)
	defer host.close()

	// A sleep gives the test a window to seed产出 into the peer job's result dir
	// before it finishes; the echo>>tracked.txt makes a git diff for E12.
	created, err := host.jobs.Submit(job.JobRequest{
		ProjectKey: "demo",
		Agent:      "exec",
		Runner:     "docker-peer",
		Cmd:        []string{"sh", "-c", "echo changed >> tracked.txt; sleep 1.5"},
		Cwd:        ".",
		TimeoutSec: 60,
	})
	if err != nil {
		t.Fatalf("host submit: %v", err)
	}

	// Find the peer's job + seed result.json + an artifact into its result dir.
	peerJobID := waitPeerJob(t, peer, 10*time.Second)
	peerJob, ok := peer.jobs.Get(peerJobID)
	if !ok || peerJob.ResultDir == "" {
		t.Fatalf("peer job result dir not available: %+v", peerJob)
	}
	resultJSON := `{"ok":true,"who":"peer"}`
	if err := os.WriteFile(filepath.Join(peerJob.ResultDir, "result.json"), []byte(resultJSON), 0o600); err != nil {
		t.Fatalf("seed peer result.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(peerJob.ResultDir, "artifacts"), 0o700); err != nil {
		t.Fatalf("mkdir peer artifacts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(peerJob.ResultDir, "artifacts", "report.txt"), []byte("data"), 0o600); err != nil {
		t.Fatalf("seed peer artifact: %v", err)
	}

	final := waitTerminal(t, host, created.ID, 20*time.Second)
	if final.Status != job.StatusDone {
		t.Fatalf("host job status=%q exit=%d err=%q, want done", final.Status, final.ExitCode, final.Error)
	}
	if final.Source != "peer:docker-peer" {
		t.Fatalf("source = %q, want peer:docker-peer", final.Source)
	}
	if final.ResultJSON != resultJSON {
		t.Fatalf("result_json = %q, want %q", final.ResultJSON, resultJSON)
	}
	if final.RenderedCommand == "" || !strings.Contains(final.RenderedCommand, "sh") {
		t.Fatalf("rendered_command = %q, want the peer-resolved sh argv", final.RenderedCommand)
	}
	if final.DiffSummary == "" || !strings.Contains(final.DiffSummary, "tracked.txt") {
		t.Fatalf("diff_summary = %q, want it to mention tracked.txt", final.DiffSummary)
	}

	// The artifacts清单回传 + is visible via the host's OWN list endpoint (reads the
	// persisted ArtifactsJSON — the host has no local artifacts dir for a peer job).
	man := listHostArtifacts(t, host, created.ID)
	if !strings.Contains(man, "report.txt") {
		t.Fatalf("host artifacts manifest missing report.txt: %q", man)
	}

	// A download of the remote artifact is a clear 409 (file留 peer 侧, v1 — D6).
	if code := downloadHostArtifactStatus(t, host, created.ID, "report.txt"); code != 409 {
		t.Fatalf("peer artifact download status = %d, want 409 (remote artifact)", code)
	}
}

// TestPeerRunnerCancelForwards starts a long-running proxied job on the host,
// cancels it via the HOST, and asserts the host job goes cancelled AND the peer
// job is also cancelled (cancel forwarded through ctx -> peer /cancel).
func TestPeerRunnerCancelForwards(t *testing.T) {
	peer := newPeerBridge(t)
	defer peer.close()
	host := newHostBridge(t, peer.srv.URL)
	defer host.close()

	created, err := host.jobs.Submit(job.JobRequest{
		ProjectKey: "demo",
		Agent:      "exec",
		Runner:     "docker-peer",
		Cmd:        []string{"sh", "-c", "echo started; sleep 30"},
		Cwd:        ".",
		TimeoutSec: 120,
	})
	if err != nil {
		t.Fatalf("host submit: %v", err)
	}

	// Wait until the host (and thus the peer) job is actually running.
	waitRunning(t, host, created.ID, 15*time.Second)

	if err := host.jobs.Cancel(created.ID); err != nil {
		t.Fatalf("host cancel: %v", err)
	}

	final := waitTerminal(t, host, created.ID, 15*time.Second)
	if final.Status != job.StatusCancelled {
		t.Fatalf("host job status=%q, want cancelled", final.Status)
	}

	// The peer job must have been cancelled too (cancel forwarded). Poll the
	// peer's index until its job reaches a terminal cancelled state.
	deadline := time.Now().Add(10 * time.Second)
	var peerCancelled bool
	for time.Now().Before(deadline) && !peerCancelled {
		peerJobs, _ := peer.jobs.ListJobs(job.ListOpts{})
		for _, pj := range peerJobs {
			if pj.Status == job.StatusCancelled {
				peerCancelled = true
			}
		}
		if !peerCancelled {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !peerCancelled {
		peerJobs, _ := peer.jobs.ListJobs(job.ListOpts{})
		t.Fatalf("peer job was not cancelled; peer jobs=%+v", peerJobs)
	}
}

// TestPeerRunnerInteractionPassthrough verifies the P9 passthrough: a peer raises
// an interaction on its running job; the host mirrors it onto its own job (host ->
// pending_interaction) via the peer SSE stream; the user answers on the HOST; and
// the runner POSTs that answer back to the peer so its interaction is answered.
func TestPeerRunnerInteractionPassthrough(t *testing.T) {
	peer := newPeerBridge(t)
	defer peer.close()
	host := newHostBridge(t, peer.srv.URL)
	defer host.close()

	created, err := host.jobs.Submit(job.JobRequest{
		ProjectKey: "demo",
		Agent:      "exec",
		Runner:     "docker-peer",
		Cmd:        []string{"sh", "-c", "echo started; sleep 30"},
		Cwd:        ".",
		TimeoutSec: 120,
	})
	if err != nil {
		t.Fatalf("host submit: %v", err)
	}
	hostJobID := created.ID
	waitRunning(t, host, hostJobID, 15*time.Second)

	// The peer worker runs exactly one job (the one the host forwarded); find it.
	peerJobID := waitPeerJob(t, peer, 10*time.Second)

	// Tear down the long-lived sleep job at the end (even on assertion failure):
	// cancel the host job (cancel is forwarded to the peer) AND wait for BOTH to
	// reach terminal so the peer's log writers are closed before t.TempDir cleanup
	// removes the job dir — otherwise RemoveAll races a still-writing goroutine.
	defer func() {
		_ = host.jobs.Cancel(hostJobID)
		waitTerminal(t, host, hostJobID, 15*time.Second)
		waitTerminal(t, peer, peerJobID, 15*time.Second)
	}()

	// Raise an interaction directly on the PEER's running job.
	createdInt, err := peer.jobs.CreateInteraction(peerJobID, job.InteractionInput{
		Type:   job.InteractionTypeQuestion,
		Prompt: "q?",
	})
	if err != nil {
		t.Fatalf("peer CreateInteraction: %v", err)
	}

	// The host must mirror that interaction onto its own job (via the SSE stream),
	// flipping the host job to pending_interaction.
	waitInteraction(t, host, hostJobID, createdInt.ID, job.InteractionPending, "", 15*time.Second)
	if hj, _ := host.jobs.Get(hostJobID); hj.Status != job.StatusPendingInteraction {
		t.Fatalf("host job status=%q, want pending_interaction", hj.Status)
	}

	// Answer on the HOST; the runner forwards the answer back to the peer.
	if _, err := host.jobs.AnswerInteraction(hostJobID, createdInt.ID, "ANS-42"); err != nil {
		t.Fatalf("host AnswerInteraction: %v", err)
	}

	// The peer's interaction must become answered with the host-supplied answer.
	waitInteraction(t, peer, peerJobID, createdInt.ID, job.InteractionAnswered, "ANS-42", 15*time.Second)
}

// waitPeerJob polls the peer bridge until exactly one job is tracked and returns
// its id (the worker only ever runs the single job the host forwarded).
func waitPeerJob(t *testing.T, b *bridge, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		jobs, _ := b.jobs.ListJobs(job.ListOpts{})
		if len(jobs) == 1 {
			return jobs[0].ID
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("peer did not register exactly one job within %s", timeout)
	return ""
}

// waitInteraction polls a bridge until the job has an interaction with the given
// id in the wanted status (and, when wantAnswer != "", that answer).
func waitInteraction(t *testing.T, b *bridge, jobID, interactionID, wantStatus, wantAnswer string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ints, _ := b.jobs.GetInteractions(jobID)
		for _, it := range ints {
			if it.ID == interactionID && it.Status == wantStatus {
				if wantAnswer == "" || it.Answer == wantAnswer {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	ints, _ := b.jobs.GetInteractions(jobID)
	t.Fatalf("interaction %q on job %q did not reach status=%q answer=%q within %s; got %+v",
		interactionID, jobID, wantStatus, wantAnswer, timeout, ints)
}

// newPeerBridgeGit is newPeerBridge whose project dir is a git repo with one
// committed tracked file, so a peer job that modifies it produces a git diff the
// peer's captureOutcomes records (E12) and回传 to the host (P4-b).
func newPeerBridgeGit(t *testing.T) *bridge {
	t.Helper()
	root := t.TempDir()
	initGitRepo(t, root)
	cfg := &config.Config{
		Server:  config.ServerConfig{AllowEmptyToken: true},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"demo": {
				HostPath:       root, // cwd "." resolves under here (the git repo)
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil)
	s := httpapi.New(&cfg.Server, "", true, jobs, projects, agents, nil, nil, nil, nil)
	return &bridge{jobs: jobs, srv: httptest.NewServer(s.Handler())}
}

// initGitRepo makes dir a git repo with a committed tracked.txt so a later
// uncommitted change shows up in `git diff` (the E12 capture baseline).
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatalf("write tracked.txt: %v", err)
	}
	run("add", "tracked.txt")
	run("commit", "-q", "-m", "init")
}

// listHostArtifacts GETs the host job's artifact manifest endpoint and returns
// the raw JSON body (the host serves it from the回传 ArtifactsJSON — it has no
// local artifacts dir for a peer job).
func listHostArtifacts(t *testing.T, host *bridge, id string) string {
	t.Helper()
	resp, err := http.Get(host.srv.URL + "/v1/jobs/" + id + "/artifacts")
	if err != nil {
		t.Fatalf("GET artifacts: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("artifacts status = %d, body = %s", resp.StatusCode, b)
	}
	return string(b)
}

// downloadHostArtifactStatus GETs a single artifact download and returns the
// status code (used to assert a remote artifact yields 409, not the bytes).
func downloadHostArtifactStatus(t *testing.T, host *bridge, id, name string) int {
	t.Helper()
	resp, err := http.Get(host.srv.URL + "/v1/jobs/" + id + "/artifacts/" + name)
	if err != nil {
		t.Fatalf("GET artifact download: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode
}

// readHostStdout reads the host job's local stdout.log via the FileStore (the
// exact read path the host /logs/stdout endpoint uses: base = dir(ResultDir)).
func readHostStdout(t *testing.T, host *bridge, id string) string {
	t.Helper()
	jr, ok := host.jobs.Get(id)
	if !ok {
		t.Fatalf("host job %s not tracked", id)
	}
	base := filepath.Dir(jr.ResultDir) // ResultDir == <base>/<id>
	b, err := store.NewFileStore(base).ReadLogTail(id, store.StreamStdout, 0)
	if err != nil {
		t.Fatalf("read host stdout: %v", err)
	}
	return string(b)
}
