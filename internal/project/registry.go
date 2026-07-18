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
	// apply is THE single serial write seam (B2). It is handed a mutation that may
	// only set/delete WHOLE entries on the projects map (never edit a value or any
	// other config field, see ApplyProjects). In a serve/mcp process core.Core
	// injects an applier that runs the mutation inside its updateMu write
	// transaction (clone → mut → save → reload → repush); a standalone CLI process
	// gets the localApply default (clone → mut → save → atomic Store), which keeps
	// the same copy-on-write safety without the reload/push machinery. Never nil
	// after NewRegistry.
	apply ApplyProjects
}

// ApplyProjects is the injected serial-write seam (B2 / plan T1-B). The applier
// owns the config write transaction; it calls mut with a NON-NIL projects map it
// may set/delete whole entries on, and — on mut success — persists and publishes
// the new config. The contract is deliberately narrow: mut MUST NOT edit a
// ProjectConfig value's fields or touch any other top-level config field, so the
// applier's one-level clone (config.Config.Clone) is sufficient.
type ApplyProjects func(mut func(projects map[string]config.ProjectConfig) error) error

// Option customises a Registry at construction.
type Option func(*Registry)

// WithProjectApplier injects the serial-write seam (core.Core.Update adapter in a
// serve/mcp process). A nil applier is ignored, leaving the localApply default —
// that is the standalone CLI degradation (`gofer project add` with no running
// core): clone + save + atomic Store, in-process, no reload/push.
func WithProjectApplier(a ApplyProjects) Option {
	return func(r *Registry) {
		if a != nil {
			r.apply = a
		}
	}
}

// NewRegistry builds a registry over cfg loaded from path. path may be empty
// (no config file yet); the localApply default resolves a user-level path on
// first write. Opts may inject a project applier (WithProjectApplier).
func NewRegistry(cfg *config.Config, path string, opts ...Option) *Registry {
	r := &Registry{path: path}
	r.cfg.Store(cfg)
	r.apply = r.localApply
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

// localApply is the default (no-core) write transaction: it clones the current
// snapshot, applies the whole-value mutation to the clone's Projects map, saves
// the clone (副本, never the live config), then atomically swaps it in. Used by
// the standalone CLI where there is no core.Core to route writes through.
func (r *Registry) localApply(mut func(projects map[string]config.ProjectConfig) error) error {
	next := r.cfg.Load().Clone()
	if next.Projects == nil {
		next.Projects = map[string]config.ProjectConfig{}
	}
	if err := mut(next.Projects); err != nil {
		return err // mut rejected (e.g. duplicate key): live config untouched
	}
	if err := r.save(next); err != nil {
		return err // save failed: do NOT publish (snapshot stays old)
	}
	r.cfg.Store(next)
	return nil
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
// The existence check and the mutation run INSIDE the apply seam's transaction
// (on the map the applier hands over), never on a pre-read snapshot: routing
// every write through the one serial transaction is what makes concurrent
// POST/DELETE lose no updates and share no Rev (B2 / plan T1-C). Add never edits
// a ProjectConfig value in place — it sets a whole entry — honouring the seam
// contract.
func (r *Registry) Add(key string, proj config.ProjectConfig, force bool) error {
	if key == "" {
		return fmt.Errorf("project key is required")
	}
	return r.apply(func(projects map[string]config.ProjectConfig) error {
		if _, exists := projects[key]; exists && !force {
			return fmt.Errorf("project %q already exists (use --force to overwrite)", key)
		}
		projects[key] = proj
		return nil
	})
}

// Remove deletes the project under key and writes back. Like Add it routes
// through the serial write seam (B2): the结论句 that only named Add left DELETE
// mutating the live map directly — so a revoked whitelist could not actually be
// revoked. Both go through apply now.
func (r *Registry) Remove(key string) error {
	return r.apply(func(projects map[string]config.ProjectConfig) error {
		if _, exists := projects[key]; !exists {
			return fmt.Errorf("unknown project %q", key)
		}
		delete(projects, key)
		return nil
	})
}

// save persists cfg (a副本 built by localApply), resolving a user-level path when
// none is known.
func (r *Registry) save(cfg *config.Config) error {
	if r.path == "" {
		p, err := config.UserConfigPath()
		if err != nil {
			return fmt.Errorf("resolve user config path: %w", err)
		}
		r.path = p
	}
	return config.Save(r.path, cfg)
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

	// Local FS checks (exec_path / exchange_dir / result_dir) only make sense when
	// the project can run on THIS node's built-in local runner. A worker-only
	// project (allowed_runners non-empty without "local", dispatched to a remote
	// worker) must NOT be probed here: checkWritableDir's MkdirAll would fabricate
	// the project tree on this machine even though the path only exists on the
	// worker's host (the worker validates its own side after roots mapping).
	if !AllowsLocalRunner(proj.AllowedRunners) {
		add("local_fs", true, fmt.Sprintf("skipped: not locally runnable (allowed_runners=%v)", proj.AllowedRunners))
	} else {
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

// AllowsLocalRunner reports whether the allowlist permits the built-in local
// runner: an empty allowlist defaults to local, otherwise it must be listed.
func AllowsLocalRunner(allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	return slices.Contains(allowed, "local")
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
	_ = os.Remove(probe) // best-effort 清理写探针文件，残留无害、无诊断价值，忽略。
	return nil
}
