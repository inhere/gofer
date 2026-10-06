package config

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func injectFixture(t *testing.T) (*Config, string) {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "ws")
	t.Setenv(EnvWorkspace, ws)
	cfg := &Config{
		Agents:   map[string]AgentConfig{"claude": {}, "exec": {}, "codex": {}},
		Projects: map[string]ProjectConfig{"mine": {HostPath: "/x"}},
	}
	return cfg, ws
}

func TestInjectDefaultProjectWhenUndeclared(t *testing.T) {
	cfg, ws := injectFixture(t)
	InjectDefaultProject(cfg)
	p, ok := cfg.Projects[DefaultProjectKey]
	if !ok || !cfg.IsInjectedProject(DefaultProjectKey) {
		t.Fatalf("default not injected/marked: %+v", cfg.Projects)
	}
	if p.HostPath != ws || p.ContainerPath != "" {
		t.Fatalf("paths = %q / %q, want host=%q only (host view)", p.HostPath, p.ContainerPath, ws)
	}
	if !reflect.DeepEqual(p.AllowedRunners, []string{BuiltinLocalRunner}) {
		t.Fatalf("runners = %v", p.AllowedRunners)
	}
	// exec is built in and never listed; the rest is sorted.
	if !reflect.DeepEqual(p.AllowedAgents, []string{"claude", "codex"}) {
		t.Fatalf("agents = %v", p.AllowedAgents)
	}
	if !reflect.DeepEqual(p, DefaultWorkspaceProject(ws, []string{"claude", "codex"})) {
		t.Fatal("injected project must equal the shared DefaultWorkspaceProject definition")
	}
	// Idempotent, and a newly detected agent is picked up on the next pass.
	cfg.Agents["omp"] = AgentConfig{}
	InjectDefaultProject(cfg)
	if got := cfg.Projects[DefaultProjectKey].AllowedAgents; !reflect.DeepEqual(got, []string{"claude", "codex", "omp"}) {
		t.Fatalf("re-inject agents = %v", got)
	}
	if len(cfg.Projects) != 2 {
		t.Fatalf("projects = %v", cfg.Projects)
	}
}

func TestInjectDefaultProjectDeclaredWins(t *testing.T) {
	cfg, _ := injectFixture(t)
	own := ProjectConfig{HostPath: "/mine", AllowedRunners: []string{"worker1"}}
	cfg.Projects[DefaultProjectKey] = own
	InjectDefaultProject(cfg)
	if cfg.IsInjectedProject(DefaultProjectKey) || !reflect.DeepEqual(cfg.Projects[DefaultProjectKey], own) {
		t.Fatalf("declared default must be kept whole and unmarked: %+v", cfg.Projects[DefaultProjectKey])
	}
	if cfg.InjectedProjects() != nil {
		t.Fatal("no injected set expected")
	}
}

func TestInjectDefaultProjectPathView(t *testing.T) {
	cfg, ws := injectFixture(t)
	cfg.Server.PathView = "container"
	InjectDefaultProject(cfg)
	p := cfg.Projects[DefaultProjectKey]
	if p.HostPath != ws || p.ContainerPath != ws || cfg.ExecPath(p) != ws {
		t.Fatalf("container view: %+v exec=%q", p, cfg.ExecPath(p))
	}
}

func TestInjectedProjectIsNotSavedAndSurvivesClone(t *testing.T) {
	cfg, _ := injectFixture(t)
	InjectDefaultProject(cfg)
	if !cfg.Clone().IsInjectedProject(DefaultProjectKey) {
		t.Fatal("clone lost the injected mark")
	}
	if _, ok := CanonicalConfig(cfg).Projects[DefaultProjectKey]; ok {
		t.Fatal("injected project leaked into the operator-owned projection (it would be saved)")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Projects[DefaultProjectKey]; ok {
		t.Fatal("injected default was written to the file")
	}
}

func TestMutateProjectsSettlesInjectedMarks(t *testing.T) {
	cfg, ws := injectFixture(t)
	InjectDefaultProject(cfg)

	// A write that leaves it alone keeps it built in.
	next := cfg.Clone()
	if err := next.MutateProjects(func(m map[string]ProjectConfig) error { m["other"] = ProjectConfig{HostPath: "/o"}; return nil }); err != nil {
		t.Fatal(err)
	}
	if !next.IsInjectedProject(DefaultProjectKey) {
		t.Fatal("unrelated write must keep default built in")
	}

	// Deleting it is refused and nothing changes.
	next = cfg.Clone()
	err := next.MutateProjects(func(m map[string]ProjectConfig) error { delete(m, DefaultProjectKey); return nil })
	if !errors.Is(err, ErrBuiltinProject) {
		t.Fatalf("delete err = %v, want ErrBuiltinProject", err)
	}
	if _, ok := next.Projects[DefaultProjectKey]; !ok || !next.IsInjectedProject(DefaultProjectKey) {
		t.Fatal("refused delete must leave the project in place")
	}

	// Editing it makes it a declared project: unmarked, and a save writes it.
	next = cfg.Clone()
	if err := next.MutateProjects(func(m map[string]ProjectConfig) error {
		p := m[DefaultProjectKey]
		p.AllowExec = true
		m[DefaultProjectKey] = p
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if next.IsInjectedProject(DefaultProjectKey) {
		t.Fatal("edited default must become declared")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, next); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Projects[DefaultProjectKey]; got.HostPath != ws || !got.AllowExec {
		t.Fatalf("saved default = %+v", got)
	}
	// Once declared, a later inject pass leaves it alone.
	InjectDefaultProject(loaded)
	if loaded.IsInjectedProject(DefaultProjectKey) {
		t.Fatal("declared default re-marked")
	}
}
