package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestBuildInjectsDefaultProjectAndNeverPersistsIt: a core built from a config without a
// `default` project serves the built-in one (after the agent resolve, so allowed_agents
// follows the detected CLIs), survives a project write + reload, and the file never
// grows a `default:` entry from an unrelated save.
func TestBuildInjectsDefaultProjectAndNeverPersistsIt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	ws := filepath.Join(dir, "ws")
	t.Setenv(config.EnvWorkspace, ws)
	cfgPath := filepath.Join(dir, "config.yaml")

	cr, err := Build(resolveTestConfig(t), WithAgentDetector(detectorFor("claude")), WithBuiltinDefaultProject())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = cr.Close() }()

	p, err := cr.Projects.Get(config.DefaultProjectKey)
	if err != nil || p.HostPath != ws {
		t.Fatalf("default = %+v err=%v", p, err)
	}
	if got := strings.Join(p.AllowedAgents, ","); got != "claude,mine" {
		t.Fatalf("allowed_agents = %q, want the detected + declared agents", got)
	}
	if !cr.Config().IsInjectedProject(config.DefaultProjectKey) {
		t.Fatal("not marked injected")
	}

	if err := cr.Projects.Add("alpha", config.ProjectConfig{HostPath: t.TempDir()}, false); err != nil {
		t.Fatalf("project add: %v", err)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "default:") {
		t.Fatalf("built-in default was persisted by an unrelated save:\n%s", raw)
	}
	// The write went through reloadLocked: the built-in is still there and still marked.
	if !cr.Config().IsInjectedProject(config.DefaultProjectKey) {
		t.Fatal("injected mark lost across the write transaction")
	}

	// Deleting it through the registry is refused.
	if err := cr.Projects.Remove(config.DefaultProjectKey); !errors.Is(err, config.ErrBuiltinProject) {
		t.Fatalf("remove err = %v, want ErrBuiltinProject", err)
	}

	// Editing it declares it: written to the file and no longer injected.
	edited := p
	edited.AllowExec = true
	if err := cr.Projects.Add(config.DefaultProjectKey, edited, true); err != nil {
		t.Fatal(err)
	}
	if cr.Config().IsInjectedProject(config.DefaultProjectKey) {
		t.Fatal("edited default must be declared")
	}
	raw, _ = os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "default:") {
		t.Fatalf("edited default not written:\n%s", raw)
	}
}

// TestBuildWithoutOptionNeverInjectsDefault: a worker builds its Core without
// WithBuiltinDefaultProject and must not grow (or advertise) a `default` project
// it was never given — on Build or on reload.
func TestBuildWithoutOptionNeverInjectsDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	t.Setenv(config.EnvWorkspace, filepath.Join(dir, "ws"))
	cr, err := Build(resolveTestConfig(t), WithAgentDetector(detectorFor("claude")))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = cr.Close() }()
	if _, ok := cr.Config().Projects[config.DefaultProjectKey]; ok {
		t.Fatal("built-in default injected without WithBuiltinDefaultProject")
	}
	if err := cr.ReloadWith(cr.Config().Clone()); err != nil {
		t.Fatalf("ReloadWith: %v", err)
	}
	if _, ok := cr.Config().Projects[config.DefaultProjectKey]; ok {
		t.Fatal("built-in default injected on reload without WithBuiltinDefaultProject")
	}
}
