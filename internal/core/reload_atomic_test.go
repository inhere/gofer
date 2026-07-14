package core

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// recordingRunner stands in for the local runner and records the command each
// job was actually going to execute. That command is the ONLY way to observe
// which agent definition Submit resolved the argv from.
type recordingRunner struct {
	mu  sync.Mutex
	cmd map[string]string // job id -> resolved command
}

func (r *recordingRunner) Name() string { return localrunner.Name }

func (r *recordingRunner) Run(_ context.Context, req runner.Request) runner.Result {
	r.mu.Lock()
	r.cmd[req.JobID] = req.Command
	r.mu.Unlock()
	return runner.Result{ExitCode: 0}
}

func (r *recordingRunner) command(jobID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cmd[jobID]
	return c, ok
}

// atomicSwapConfig builds one VERSION of the test config. Both versions accept
// the exact same job, but every config-derived fact Submit consumes is tagged
// with the version:
//
//   - storage.root  → read from the config SNAPSHOT Submit takes at entry (it
//     drives validate + the job's result dir). This is the "policy" side.
//   - agents[a].command → read when the executable argv is BUILT. This is the
//     "agent definition" side.
//
// A submit that ends up with a result dir under root vN but a command from
// version M != N is exactly the torn read this test exists to catch: it passed
// admission against one config and was built from another.
func atomicSwapConfig(root, hostPath, dbPath, version string) *config.Config {
	return &config.Config{
		Storage: config.StorageConfig{Root: root, DBPath: dbPath},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       hostPath,
				AllowedAgents:  []string{"a"},
				AllowedRunners: []string{localrunner.Name},
			},
		},
		Agents: map[string]config.AgentConfig{
			"a": {Type: "cli-agent", Command: "cmd-" + version, Args: []string{"{{prompt}}"}},
		},
	}
}

// TestReloadWithConfigSnapshotIsAtomic hammers Submit from N goroutines while a
// single goroutine loops Core.ReloadWith between two visibly different configs
// (different storage root AND a different command for the same agent key).
//
// Invariant (plan 验收 8): every Submit observes ONE config version end to end.
// The config it validated against (and therefore the result dir it was given)
// and the agent definition its command was built from must be the same version
// — never a mix of "passed the old policy, executes the new agent's command".
//
// This asserts a LOGICAL invariant, not a memory race: both sides are read
// through atomic.Pointer loads, so the race detector alone cannot see the bug.
// It is still run under -race to cover the swap itself.
func TestReloadWithConfigSnapshotIsAtomic(t *testing.T) {
	hostPath := t.TempDir()
	rootV1, rootV2 := t.TempDir(), t.TempDir()
	// One db for both versions: a reload never reopens the metadata store, so the
	// db path must not move with the config version.
	dbPath := filepath.Join(t.TempDir(), "gofer.db")

	cfgV1 := atomicSwapConfig(rootV1, hostPath, dbPath, "v1")
	cfgV2 := atomicSwapConfig(rootV2, hostPath, dbPath, "v2")

	c, err := Build(cfgV1, WithAgentDetector(agent.NoopDetector{}))
	if err != nil {
		t.Fatalf("build core: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Swap the local runner for the recorder BEFORE any goroutine starts (the map
	// Build hands to job.Service is this very map, so the service sees it too).
	rec := &recordingRunner{cmd: map[string]string{}}
	c.Runners[localrunner.Name] = rec

	// versionOf maps a config-derived fact back to the version that produced it.
	versionOf := func(resultDir, command string) (string, string) {
		var rootVer string
		switch {
		case strings.HasPrefix(resultDir, rootV1):
			rootVer = "v1"
		case strings.HasPrefix(resultDir, rootV2):
			rootVer = "v2"
		default:
			rootVer = "?" + resultDir
		}
		return rootVer, strings.TrimPrefix(command, "cmd-")
	}

	stop := make(chan struct{})
	reloaderDone := make(chan struct{})
	go func() {
		defer close(reloaderDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			cfg := cfgV1
			if i%2 == 1 {
				cfg = cfgV2
			}
			if err := c.ReloadWith(cfg); err != nil {
				panic("ReloadWith: " + err.Error())
			}
		}
	}()

	const submitters, perSubmitter = 4, 60
	type sample struct{ id, resultDir string }
	var mu sync.Mutex
	var samples []sample

	var wg sync.WaitGroup
	for w := 0; w < submitters; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perSubmitter; i++ {
				// Both config versions accept this job in full, so ANY error means a
				// submit saw an inconsistent view of the config.
				res, err := c.Jobs.Submit(job.JobRequest{
					ProjectKey: "self", Agent: "a", Runner: localrunner.Name,
					Prompt: "hello", Cwd: ".", TimeoutSec: 30,
				})
				if err != nil {
					mu.Lock()
					samples = append(samples, sample{id: "", resultDir: "submit error: " + err.Error()})
					mu.Unlock()
					continue
				}
				mu.Lock()
				samples = append(samples, sample{id: res.ID, resultDir: res.ResultDir})
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(stop)
	<-reloaderDone

	// Drain every launched job so its command has certainly been recorded (and no
	// goroutine outlives the temp dirs).
	seen := map[string]int{}
	for _, s := range samples {
		if s.id == "" {
			t.Fatalf("submit failed while reloading (torn config view): %s", s.resultDir)
		}
		c.Jobs.Wait(s.id)
		cmd, ok := rec.command(s.id)
		if !ok {
			t.Fatalf("job %s never reached the runner", s.id)
		}
		rootVer, cmdVer := versionOf(s.resultDir, cmd)
		if rootVer != cmdVer {
			t.Fatalf("job %s mixed two config versions: validated/result-dir from %q (%s) but command built from %q (%s)",
				s.id, rootVer, s.resultDir, cmdVer, cmd)
		}
		seen[rootVer]++
	}

	// A green run only proves something if BOTH configs were actually in force
	// during the submit storm.
	if seen["v1"] == 0 || seen["v2"] == 0 {
		t.Fatalf("test never exercised both config versions (v1=%d v2=%d) — it proves nothing", seen["v1"], seen["v2"])
	}
}
