package peerhttp_test

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dev-agent-bridge/internal/agent"
	"dev-agent-bridge/internal/config"
	"dev-agent-bridge/internal/httpapi"
	"dev-agent-bridge/internal/job"
	"dev-agent-bridge/internal/project"
	"dev-agent-bridge/internal/runner"
	localrunner "dev-agent-bridge/internal/runner/local"
	"dev-agent-bridge/internal/runner/peerhttp"
	"dev-agent-bridge/internal/store"
)

// bridge bundles a wired job.Service + httpapi.Server for one node (host/peer).
type bridge struct {
	jobs *job.Service
	srv  *httptest.Server
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
	jobs := job.NewService(cfg, projects, agents, runners)
	s := httpapi.New(&cfg.Server, "", true, jobs, projects, agents)
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
	jobs := job.NewService(cfg, projects, agents, runners)
	s := httpapi.New(&cfg.Server, "", true, jobs, projects, agents)
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
