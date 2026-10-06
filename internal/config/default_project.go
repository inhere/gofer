package config

import (
	"errors"
	"reflect"
	"sort"
)

// DefaultProjectKey is the key of the default-workspace project: the one project every
// gofer server can count on (`gofer init` writes it, and a server whose config does
// not declare it gets the built-in one injected, InjectDefaultProject).
const DefaultProjectKey = "default"

// BuiltinExecAgentKey is the always-available exec agent. It needs no declaration, so
// the default project's allowed_agents never lists it.
const BuiltinExecAgentKey = "exec"

// DefaultWorkspaceProject is THE definition of the default-workspace project, shared
// by `gofer init server` (which writes it into config.yaml) and InjectDefaultProject
// (which synthesizes it at runtime): the directory itself, the local runner, and the
// given agents (an EMPTY allowed_agents means "any configured agent", so a host with
// no CLI installed still gets a usable project).
func DefaultWorkspaceProject(hostPath string, agents []string) ProjectConfig {
	p := ProjectConfig{
		HostPath:       hostPath,
		AllowedRunners: []string{BuiltinLocalRunner},
	}
	if len(agents) > 0 {
		p.AllowedAgents = agents
	}
	return p
}

// InjectDefaultProject materializes the built-in `default` project when the operator
// did not declare one. Like agent.Resolve it runs on every config snapshot (build and
// reload), BEFORE the snapshot is published, and is idempotent: a previously injected
// project is dropped first and re-derived, so an agent installed since is picked up.
//
// The project points at the server's default workspace (WorkspaceDir(""), so
// GOFER_WORKSPACE applies). It is a server-side path, resolved by this process (G002):
// host_path is always set, container_path additionally under server.path_view=container
// so ExecPath returns it. The directory is created by the startup workspace ensure.
// An operator-declared `default` wins whole and is never marked.
func InjectDefaultProject(cfg *Config) {
	if cfg == nil {
		return
	}
	for key := range cfg.injectedProjects {
		delete(cfg.Projects, key)
	}
	cfg.injectedProjects = nil
	if _, declared := cfg.Projects[DefaultProjectKey]; declared {
		return
	}
	dir, err := WorkspaceDir("")
	if err != nil {
		return // no resolvable home: nothing sensible to inject
	}
	agents := make([]string, 0, len(cfg.Agents))
	for key := range cfg.Agents {
		if key != BuiltinExecAgentKey {
			agents = append(agents, key)
		}
	}
	sort.Strings(agents)
	p := DefaultWorkspaceProject(dir, agents)
	if cfg.Server.PathView == "container" {
		p.ContainerPath = dir
	}
	if cfg.Projects == nil {
		cfg.Projects = map[string]ProjectConfig{}
	}
	cfg.Projects[DefaultProjectKey] = p
	cfg.injectedProjects = map[string]bool{DefaultProjectKey: true}
}

// IsInjectedProject reports whether key is a built-in project the operator did not
// declare.
func (c *Config) IsInjectedProject(key string) bool {
	return c != nil && c.injectedProjects[key]
}

// InjectedProjects returns a copy of the injected project keys (nil if none).
func (c *Config) InjectedProjects() map[string]bool {
	if c == nil || len(c.injectedProjects) == 0 {
		return nil
	}
	out := make(map[string]bool, len(c.injectedProjects))
	for k := range c.injectedProjects {
		out[k] = true
	}
	return out
}

// MutateProjects runs a project write on c.Projects (the seam every project add /
// remove goes through) and then settles the injected marks: an injected project whose
// entry the write changed is now operator-authored, so it is unmarked and a save writes
// it into the file ("保存后写入配置成为声明项目"); an unchanged one stays built in.
// Deleting an injected project is refused — it would only reappear on the next reload.
func (c *Config) MutateProjects(mut func(map[string]ProjectConfig) error) error {
	if c.Projects == nil {
		c.Projects = map[string]ProjectConfig{}
	}
	before := make(map[string]ProjectConfig, len(c.injectedProjects))
	for key := range c.injectedProjects {
		before[key] = c.Projects[key]
	}
	if err := mut(c.Projects); err != nil {
		return err
	}
	for key, old := range before {
		now, ok := c.Projects[key]
		if !ok {
			c.Projects[key] = old // a write cannot drop a built-in project
			return ErrBuiltinProject
		}
		if !reflect.DeepEqual(now, old) {
			delete(c.injectedProjects, key)
		}
	}
	if len(c.injectedProjects) == 0 {
		c.injectedProjects = nil
	}
	return nil
}

// ErrBuiltinProject is returned when a write would delete a built-in project.
var ErrBuiltinProject = errors.New("内置项目不能删除（可在配置里显式声明同名项目来覆盖）")
