package project

import (
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestReloadAddsAndRemovesProject verifies a Reload swaps the visible project
// set: Get returns a newly-added project and rejects a removed one (C3).
func TestReloadAddsAndRemovesProject(t *testing.T) {
	old := &config.Config{Projects: map[string]config.ProjectConfig{
		"alpha": {HostPath: "/tmp/alpha"},
	}}
	reg := NewRegistry(old, "")

	if _, err := reg.Get("alpha"); err != nil {
		t.Fatalf("alpha should exist before reload: %v", err)
	}
	if _, err := reg.Get("beta"); err == nil {
		t.Fatal("beta should not exist before reload")
	}

	// New config drops alpha, adds beta.
	next := &config.Config{Projects: map[string]config.ProjectConfig{
		"beta": {HostPath: "/tmp/beta"},
	}}
	reg.Reload(next)

	if _, err := reg.Get("beta"); err != nil {
		t.Fatalf("beta should exist after reload: %v", err)
	}
	if _, err := reg.Get("alpha"); err == nil {
		t.Fatal("alpha should be gone after reload")
	}
	// List reflects the new config too.
	keys := reg.List()
	if len(keys) != 1 || keys[0] != "beta" {
		t.Fatalf("List after reload = %v, want [beta]", keys)
	}
}

// TestReloadConcurrentGetRace spins concurrent Get callers while another
// goroutine repeatedly Reloads. Run with -race: there must be no data race and
// no panic. Every Get must observe a self-consistent config snapshot.
func TestReloadConcurrentGetRace(t *testing.T) {
	cfgs := []*config.Config{
		{Projects: map[string]config.ProjectConfig{"a": {HostPath: "/tmp/a"}}},
		{Projects: map[string]config.ProjectConfig{"b": {HostPath: "/tmp/b"}}},
		{Projects: map[string]config.ProjectConfig{"a": {HostPath: "/tmp/a"}, "b": {HostPath: "/tmp/b"}}},
	}
	reg := NewRegistry(cfgs[0], "")

	stop := make(chan struct{})
	reloaderDone := make(chan struct{})

	// Reloader: cycle through the configs until readers are done.
	go func() {
		defer close(reloaderDone)
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				reg.Reload(cfgs[i%len(cfgs)])
				i++
			}
		}
	}()

	// Readers.
	var readers sync.WaitGroup
	for r := 0; r < 8; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for j := 0; j < 5000; j++ {
				_, _ = reg.Get("a")
				_, _ = reg.Get("b")
				_ = reg.List()
				_ = reg.Config()
			}
		}()
	}

	// Wait for readers, then stop the reloader and wait for it to exit.
	readers.Wait()
	close(stop)
	<-reloaderDone
}
