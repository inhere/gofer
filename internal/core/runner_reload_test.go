package core

import (
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

func TestReloadAddsWorkerRunner(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	base := &config.Config{
		Storage:  config.StorageConfig{Root: filepath.Join(root, "store")},
		Server:   config.ServerConfig{Workers: map[string]config.WorkerAuthConfig{"w-third": {Token: "token-3"}}},
		Projects: map[string]config.ProjectConfig{"self": {HostPath: root, AllowedAgents: []string{"exec"}}},
		Runners:  map[string]config.RunnerConfig{},
	}
	writeConfig(t, path, base)
	loaded, _, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cr, err := Build(loaded, WithConfigPath(path), WithAgentDetector(agent.NoopDetector{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cr.Close() }()

	next := base.Clone()
	next.Runners = map[string]config.RunnerConfig{
		"w-third": {Type: "worker", WorkerID: "w-third"},
	}
	next.Projects["self"] = config.ProjectConfig{HostPath: root, AllowedAgents: []string{"exec"}, AllowedRunners: []string{"w-third"}, DefaultAgent: "exec"}
	writeConfig(t, path, next)
	if _, err := cr.ReloadDetailed(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := cr.Runners["w-third"]; !ok {
		t.Fatal("worker runner w-third was not added by reload")
	}
	if _, err := cr.Jobs.Submit(job.JobRequest{ProjectKey: "self", Agent: "exec", Runner: "w-third", Prompt: "reload runner admission", Cwd: ".", TimeoutSec: 5}); err != nil {
		t.Fatalf("new worker runner was not admitted for dispatch: %v", err)
	}
}

func TestReloadRemovesWorkerRunner(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	base := &config.Config{
		Storage: config.StorageConfig{Root: filepath.Join(root, "store")},
		Server:  config.ServerConfig{Workers: map[string]config.WorkerAuthConfig{"w-third": {Token: "token-3"}}},
		Runners: map[string]config.RunnerConfig{"w-third": {Type: "worker", WorkerID: "w-third"}},
	}
	writeConfig(t, path, base)
	loaded, _, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cr, err := Build(loaded, WithConfigPath(path), WithAgentDetector(agent.NoopDetector{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cr.Close() }()

	next := base.Clone()
	next.Runners = nil
	writeConfig(t, path, next)
	if _, err := cr.ReloadDetailed(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := cr.Runners["w-third"]; ok {
		t.Fatal("worker runner w-third remained after reload removal")
	}
}
