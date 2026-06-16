package project

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"dev-agent-bridge/internal/config"
)

// Registry manages project entries over a loaded config and persists changes
// back to the config file via config.Save.
type Registry struct {
	cfg  *config.Config
	path string // config file path used for write-back; may be "" until Add picks one
}

// NewRegistry builds a registry over cfg loaded from path. path may be empty
// (no config file yet); Add will resolve a user-level path on first write.
func NewRegistry(cfg *config.Config, path string) *Registry {
	return &Registry{cfg: cfg, path: path}
}

// Config exposes the underlying config (read-only intent).
func (r *Registry) Config() *config.Config { return r.cfg }

// Path returns the config file path used for write-back.
func (r *Registry) Path() string { return r.path }

// List returns project keys sorted for stable output.
func (r *Registry) List() []string {
	keys := make([]string, 0, len(r.cfg.Projects))
	for k := range r.cfg.Projects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Get returns the project config for key, or an error if not registered.
func (r *Registry) Get(key string) (config.ProjectConfig, error) {
	p, ok := r.cfg.Projects[key]
	if !ok {
		return config.ProjectConfig{}, fmt.Errorf("unknown project %q", key)
	}
	return p, nil
}

// Add registers proj under key and writes the config back. It refuses to
// overwrite an existing key unless force is true.
func (r *Registry) Add(key string, proj config.ProjectConfig, force bool) error {
	if key == "" {
		return fmt.Errorf("project key is required")
	}
	if _, exists := r.cfg.Projects[key]; exists && !force {
		return fmt.Errorf("project %q already exists (use --force to overwrite)", key)
	}
	if r.cfg.Projects == nil {
		r.cfg.Projects = map[string]config.ProjectConfig{}
	}
	r.cfg.Projects[key] = proj
	return r.save()
}

// Remove deletes the project under key and writes back.
func (r *Registry) Remove(key string) error {
	if _, exists := r.cfg.Projects[key]; !exists {
		return fmt.Errorf("unknown project %q", key)
	}
	delete(r.cfg.Projects, key)
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
	return config.Save(r.path, r.cfg)
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
	proj, err := r.Get(key)
	if err != nil {
		return nil, false, err
	}

	var results []CheckResult
	ok := true
	add := func(name string, pass bool, info string) {
		results = append(results, CheckResult{Name: name, OK: pass, Info: info})
		if !pass {
			ok = false
		}
	}

	// host_path exists and is a directory.
	hostAbs, _ := filepath.Abs(proj.HostPath)
	if fi, statErr := os.Stat(hostAbs); statErr != nil {
		add("host_path", false, fmt.Sprintf("%s: %v", hostAbs, statErr))
	} else if !fi.IsDir() {
		add("host_path", false, fmt.Sprintf("%s is not a directory", hostAbs))
	} else {
		add("host_path", true, hostAbs)
	}

	// exchange dir creatable/writable.
	if exDir, exErr := ExchangeDir(r.cfg, proj); exErr != nil {
		add("exchange_dir", false, exErr.Error())
	} else if wErr := checkWritableDir(exDir); wErr != nil {
		add("exchange_dir", false, fmt.Sprintf("%s: %v", exDir, wErr))
	} else {
		add("exchange_dir", true, exDir)
	}

	// result base dir creatable/writable (covers storage.root branch too).
	if resDir, resErr := ResultBaseDir(r.cfg, key, proj); resErr != nil {
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
		if !r.agentDefined(proj.DefaultAgent) {
			add("default_agent", false, fmt.Sprintf("agent %q not defined", proj.DefaultAgent))
		} else {
			add("default_agent", true, proj.DefaultAgent)
		}
	}
	for _, a := range proj.AllowedAgents {
		if !r.agentDefined(a) {
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
		if _, exists := r.cfg.Runners[rn]; !exists {
			add("allowed_runner:"+rn, false, fmt.Sprintf("runner %q not defined", rn))
		} else {
			add("allowed_runner:"+rn, true, rn)
		}
	}

	return results, ok, nil
}

// builtinAgents are agent keys that are always valid without a config entry.
var builtinAgents = map[string]bool{"exec": true}

// agentDefined reports whether an agent key is a built-in or declared in config.
func (r *Registry) agentDefined(key string) bool {
	if builtinAgents[key] {
		return true
	}
	_, exists := r.cfg.Agents[key]
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
