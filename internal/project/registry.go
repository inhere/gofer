package project

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync/atomic"

	"github.com/inhere/gofer/internal/config"
)

// Registry manages project entries over a loaded config and persists changes
// back to the config file via config.Save.
//
// The config is held behind an atomic.Pointer so SIGHUP-driven hot-reload (C3)
// can atomically swap in a freshly loaded config without locking every read
// path. Each read takes one snapshot (cfg.Load()) and uses it for the whole
// call so a concurrent Reload can never make a single call observe two configs.
type Registry struct {
	cfg  atomic.Pointer[config.Config]
	path string // config file path used for write-back; may be "" until Add picks one
}

// NewRegistry builds a registry over cfg loaded from path. path may be empty
// (no config file yet); Add will resolve a user-level path on first write.
func NewRegistry(cfg *config.Config, path string) *Registry {
	r := &Registry{path: path}
	r.cfg.Store(cfg)
	return r
}

// Config exposes the current config snapshot (read-only intent).
func (r *Registry) Config() *config.Config { return r.cfg.Load() }

// Reload atomically swaps the registry's config to newCfg (C3 hot-reload). It
// is safe to call concurrently with any read path; in-flight reads keep using
// the snapshot they already loaded.
func (r *Registry) Reload(newCfg *config.Config) { r.cfg.Store(newCfg) }

// Path returns the config file path used for write-back.
func (r *Registry) Path() string { return r.path }

// List returns project keys sorted for stable output.
func (r *Registry) List() []string {
	cfg := r.cfg.Load()
	keys := make([]string, 0, len(cfg.Projects))
	for k := range cfg.Projects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Get returns the project config for key, or an error if not registered.
func (r *Registry) Get(key string) (config.ProjectConfig, error) {
	cfg := r.cfg.Load()
	p, ok := cfg.Projects[key]
	if !ok {
		return config.ProjectConfig{}, fmt.Errorf("unknown project %q", key)
	}
	return p, nil
}

// Add registers proj under key and writes the config back. It refuses to
// overwrite an existing key unless force is true.
//
// Add mutates the current config in place (the same struct the atomic pointer
// references) and persists it; it is a CLI write path (`gofer project add`),
// not a concurrent hot path, so no extra synchronisation is needed beyond the
// atomic pointer the read paths use.
func (r *Registry) Add(key string, proj config.ProjectConfig, force bool) error {
	if key == "" {
		return fmt.Errorf("project key is required")
	}
	cfg := r.cfg.Load()
	if _, exists := cfg.Projects[key]; exists && !force {
		return fmt.Errorf("project %q already exists (use --force to overwrite)", key)
	}
	if cfg.Projects == nil {
		cfg.Projects = map[string]config.ProjectConfig{}
	}
	cfg.Projects[key] = proj
	return r.save()
}

// Remove deletes the project under key and writes back.
func (r *Registry) Remove(key string) error {
	cfg := r.cfg.Load()
	if _, exists := cfg.Projects[key]; !exists {
		return fmt.Errorf("unknown project %q", key)
	}
	delete(cfg.Projects, key)
	return r.save()
}

// save persists the config, resolving a user-level path when none is known.
func (r *Registry) save() error {
	if r.path == "" {
		p, err := config.UserConfigPath()
		if err != nil {
			return fmt.Errorf("resolve user config path: %w", err)
		}
		r.path = p
	}
	return config.Save(r.path, r.cfg.Load())
}

// CheckResult is a single named validation outcome.
type CheckResult struct {
	Name string
	OK   bool
	Info string
}

// Validate runs filesystem and reference checks for a project and returns each
// check's result. The boolean reports overall pass/fail.
func (r *Registry) Validate(key string) ([]CheckResult, bool, error) {
	// Snapshot the config once for the whole validation so a concurrent Reload
	// cannot make a single Validate observe two different configs.
	cfg := r.cfg.Load()
	proj, found := cfg.Projects[key]
	if !found {
		return nil, false, fmt.Errorf("unknown project %q", key)
	}

	var results []CheckResult
	ok := true
	add := func(name string, pass bool, info string) {
		results = append(results, CheckResult{Name: name, OK: pass, Info: info})
		if !pass {
			ok = false
		}
	}

	// exec_path (E29/D10: the gofer-process execution root, = host_path by default,
	// or container_path under server.path_view=container) exists and is a directory.
	// This validates the path gofer can actually reach to run jobs.
	execAbs, _ := filepath.Abs(cfg.ExecPath(proj))
	if fi, statErr := os.Stat(execAbs); statErr != nil {
		add("exec_path", false, fmt.Sprintf("%s: %v", execAbs, statErr))
	} else if !fi.IsDir() {
		add("exec_path", false, fmt.Sprintf("%s is not a directory", execAbs))
	} else {
		add("exec_path", true, execAbs)
	}

	// exchange dir creatable/writable.
	if exDir, exErr := ExchangeDir(cfg, proj); exErr != nil {
		add("exchange_dir", false, exErr.Error())
	} else if wErr := checkWritableDir(exDir); wErr != nil {
		add("exchange_dir", false, fmt.Sprintf("%s: %v", exDir, wErr))
	} else {
		add("exchange_dir", true, exDir)
	}

	// result base dir creatable/writable (covers storage.root branch too).
	if resDir, resErr := ResultBaseDir(cfg, key, proj); resErr != nil {
		add("result_dir", false, resErr.Error())
	} else if wErr := checkWritableDir(resDir); wErr != nil {
		add("result_dir", false, fmt.Sprintf("%s: %v", resDir, wErr))
	} else {
		add("result_dir", true, resDir)
	}

	// default_agent / allowed_agents references exist in config (if non-empty).
	// "exec" is a built-in agent (P3 defines its semantics) and need not be
	// declared in config.Agents, mirroring the built-in "local" runner.
	if proj.DefaultAgent != "" {
		if !agentDefined(cfg, proj.DefaultAgent) {
			add("default_agent", false, fmt.Sprintf("agent %q not defined", proj.DefaultAgent))
		} else if len(proj.AllowedAgents) > 0 && !slices.Contains(proj.AllowedAgents, proj.DefaultAgent) {
			// D5: default_agent must be within allowed_agents when admission is
			// restricted, else an overlay could borrow default_agent to bypass it.
			add("default_agent", false, fmt.Sprintf("agent %q not in allowed_agents %v (D2)", proj.DefaultAgent, proj.AllowedAgents))
		} else {
			add("default_agent", true, proj.DefaultAgent)
		}
	}
	for _, a := range proj.AllowedAgents {
		if !agentDefined(cfg, a) {
			add("allowed_agent:"+a, false, fmt.Sprintf("agent %q not defined", a))
		} else {
			add("allowed_agent:"+a, true, a)
		}
	}
	for _, rn := range proj.AllowedRunners {
		// "local" is a built-in runner and need not be declared in config.Runners.
		if rn == "local" {
			add("allowed_runner:"+rn, true, "builtin")
			continue
		}
		if _, exists := cfg.Runners[rn]; !exists {
			add("allowed_runner:"+rn, false, fmt.Sprintf("runner %q not defined", rn))
		} else {
			add("allowed_runner:"+rn, true, rn)
		}
	}

	return results, ok, nil
}

// builtinAgents are agent keys that are always valid without a config entry.
var builtinAgents = map[string]bool{"exec": true}

// agentDefined reports whether an agent key is a built-in or declared in the
// given config snapshot.
func agentDefined(cfg *config.Config, key string) bool {
	if builtinAgents[key] {
		return true
	}
	_, exists := cfg.Agents[key]
	return exists
}

// checkWritableDir ensures dir can be created and written to. It creates the dir
// (and parents) if missing and probes write access with a temp file.
func checkWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	probe := filepath.Join(dir, ".dab-write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	_ = os.Remove(probe)
	return nil
}
