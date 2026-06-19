package agent

import (
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestReloadAddsAndRemovesAgent verifies a Reload swaps the visible agent set:
// Get returns a newly-added agent and rejects a removed one (the built-in exec
// always resolves regardless) (C3).
func TestReloadAddsAndRemovesAgent(t *testing.T) {
	old := &config.Config{Agents: map[string]config.AgentConfig{
		"codex": {Type: TypeCLIAgent, Command: "codex"},
	}}
	reg := NewRegistry(old)

	if _, ok := reg.Get("codex"); !ok {
		t.Fatal("codex should exist before reload")
	}
	if _, ok := reg.Get("claude"); ok {
		t.Fatal("claude should not exist before reload")
	}

	next := &config.Config{Agents: map[string]config.AgentConfig{
		"claude": {Type: TypeCLIAgent, Command: "claude"},
	}}
	reg.Reload(next)

	if _, ok := reg.Get("claude"); !ok {
		t.Fatal("claude should exist after reload")
	}
	if _, ok := reg.Get("codex"); ok {
		t.Fatal("codex should be gone after reload")
	}
	// Built-in exec still resolves regardless of config contents.
	if _, ok := reg.Get(ExecAgentKey); !ok {
		t.Fatal("built-in exec must always resolve")
	}
}

// TestReloadConcurrentGetRace spins concurrent Get/Names callers while another
// goroutine repeatedly Reloads. Run with -race: no data race, no panic.
func TestReloadConcurrentGetRace(t *testing.T) {
	cfgs := []*config.Config{
		{Agents: map[string]config.AgentConfig{"codex": {Type: TypeCLIAgent, Command: "codex"}}},
		{Agents: map[string]config.AgentConfig{"claude": {Type: TypeCLIAgent, Command: "claude"}}},
		{Agents: map[string]config.AgentConfig{}},
	}
	reg := NewRegistry(cfgs[0])

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
				reg.Reload(cfgs[i%len(cfgs)])
				i++
			}
		}
	}()

	var readers sync.WaitGroup
	for r := 0; r < 8; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for j := 0; j < 5000; j++ {
				_, _ = reg.Get("codex")
				_, _ = reg.Get("claude")
				_, _ = reg.Get(ExecAgentKey)
				_ = reg.Names()
			}
		}()
	}

	readers.Wait()
	close(stop)
	<-reloaderDone
}
