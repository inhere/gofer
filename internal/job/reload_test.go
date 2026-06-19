package job

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// projectCfg builds a permissive exec-capable project rooted at root.
func projectCfg(root string) config.ProjectConfig {
	return config.ProjectConfig{
		HostPath:       root,
		AllowedAgents:  []string{"exec"},
		AllowedRunners: []string{"local"},
		AllowExec:      true,
	}
}

// TestReloadAddsProjectThenSubmit verifies a project added by a Reload becomes
// submittable without a restart (C3).
func TestReloadAddsProjectThenSubmit(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	// "added" does not exist yet -> submit must fail with ErrUnknownProject.
	_, err := s.Submit(JobRequest{
		ProjectKey: "added", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if !errors.Is(err, ErrUnknownProject) {
		t.Fatalf("submit to unknown project: got %v, want ErrUnknownProject", err)
	}

	// Reload a config that adds "added" (keep storage.root so result dirs stay
	// under root).
	next := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self":  projectCfg(root),
			"added": projectCfg(root),
		},
	}
	s.Reload(next)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "added", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("job on reloaded project status = %s, want done", final.Status)
	}
}

// TestReloadRemovesProjectRejectsSubmit verifies a project removed by a Reload
// is no longer submittable (returns ErrUnknownProject) (C3).
func TestReloadRemovesProjectRejectsSubmit(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	// Sanity: "self" works before reload.
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("pre-reload job status = %s, want done", final.Status)
	}

	// Reload a config WITHOUT "self".
	next := &config.Config{
		Storage:  config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"other": projectCfg(root)},
	}
	s.Reload(next)

	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if !errors.Is(err, ErrUnknownProject) {
		t.Fatalf("submit to removed project: got %v, want ErrUnknownProject", err)
	}
}

// TestReloadMidFlightDoesNotBreakRunningJob verifies an in-flight job submitted
// before a Reload still completes normally, even if the reload removes its
// project (the running job already captured its config snapshot) (C3).
func TestReloadMidFlightDoesNotBreakRunningJob(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	// Submit a short-sleeping job, then reload (removing its project) while it
	// runs. The job must still finish successfully.
	r, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "1"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForStatus(t, s, r.ID, StatusRunning, 2*time.Second)

	// Reload removing "self" mid-flight.
	next := &config.Config{
		Storage:  config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"other": projectCfg(root)},
	}
	s.Reload(next)

	final, ok := s.Wait(r.ID)
	if !ok {
		t.Fatal("in-flight job not found after reload")
	}
	if final.Status != StatusDone {
		t.Fatalf("in-flight job status after reload = %s, want done", final.Status)
	}
}

// TestReloadConcurrentSubmitRace runs a Submit loop alongside a Reload loop.
// Run with -race: no data race, no panic. The job service must stay consistent
// while its config pointer is swapped under concurrent submits.
func TestReloadConcurrentSubmitRace(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	cfgs := []*config.Config{
		{Storage: config.StorageConfig{Root: root}, Projects: map[string]config.ProjectConfig{"self": projectCfg(root)}},
		{Storage: config.StorageConfig{Root: root}, Projects: map[string]config.ProjectConfig{"self": projectCfg(root), "extra": projectCfg(root)}},
		{Storage: config.StorageConfig{Root: root}, Projects: map[string]config.ProjectConfig{"extra": projectCfg(root)}},
	}

	stop := make(chan struct{})
	reloaderDone := make(chan struct{})
	go func() {
		defer close(reloaderDone)
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				s.Reload(cfgs[i%len(cfgs)])
				i++
			}
		}
	}()

	// Submitters: fire fast no-op jobs. Some submits succeed, some hit
	// ErrUnknownProject depending on the currently-loaded config; either is fine
	// — the test asserts only the absence of races/panics. Collect ids to drain.
	var idsMu sync.Mutex
	var ids []string
	var submitters sync.WaitGroup
	for w := 0; w < 4; w++ {
		submitters.Add(1)
		go func() {
			defer submitters.Done()
			for j := 0; j < 30; j++ {
				res, err := s.Submit(JobRequest{
					ProjectKey: "self", Agent: "exec", Runner: "local",
					Cmd: []string{"true"}, Cwd: ".", TimeoutSec: 30,
				})
				if err == nil {
					idsMu.Lock()
					ids = append(ids, res.ID)
					idsMu.Unlock()
				}
			}
		}()
	}

	submitters.Wait()
	close(stop)
	<-reloaderDone

	// Drain launched jobs so their goroutines stop before TempDir cleanup.
	for _, id := range ids {
		s.Wait(id)
	}
}
