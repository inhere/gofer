package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	yaml "github.com/goccy/go-yaml"
)

// EnvConfigPath is the env var consulted in the config lookup chain (§6.1).
const EnvConfigPath = "GOFER_CONFIG"

// EnvConfigDir overrides the user-level config directory. When set, both the
// default config file (<dir>/config.yaml) and the global dotenv (<dir>/.env)
// resolve under it instead of ~/.config/gofer.
const EnvConfigDir = "GOFER_CONFIG_DIR"

// DefaultConfigDirName is the user-level config dir under the OS config home
// (~/.config/<name>).
const DefaultConfigDirName = "gofer"

// EnvRunMode declares a node's ROLE so role-default commands pick the matching
// LOCAL config file: "server" (default) → config.yaml; "worker" → worker.yaml.
// E38②. mcp is intentionally NOT a value here — standalone mcp still loads
// config.yaml (it executes jobs in-process), and client-mode mcp's config is
// governed by its --server flag (E28), not by this role.
const EnvRunMode = "GOFER_RUN_MODE"

// Run-mode values for EnvRunMode.
const (
	RunModeServer = "server"
	RunModeWorker = "worker"
)

// RunMode returns the node role from GOFER_RUN_MODE: RunModeWorker when the env
// is set to "worker" (case-insensitive), else RunModeServer (the default for any
// empty/other value). Role-default commands (e.g. `project list`) use it to read
// the matching local config (config.yaml vs worker.yaml).
func RunMode() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(EnvRunMode)), RunModeWorker) {
		return RunModeWorker
	}
	return RunModeServer
}

// CurrentDirConfigNames are the per-directory config file names, in priority
// order (§6.1): a local override (.gofer.local.yaml, gitignored) takes
// precedence over the shared .gofer.yaml when both exist in the cwd.
var CurrentDirConfigNames = []string{".gofer.local.yaml", ".gofer.yaml"}

// Load resolves the config file via the lookup chain, decodes it into the
// strongly-typed Config, applies defaults and runs basic validation. It returns
// the loaded config and the resolved absolute path.
//
// Lookup order (§6.1):
//  1. explicitPath (CLI --config)
//  2. env GOFER_CONFIG
//  3. ./.gofer[.local].yaml
//  4. ~/.config/gofer/config.yaml
//
// When no file is found, Load returns a defaulted empty Config and an empty
// path (no error) so that `project add` can create the first config.
func Load(explicitPath string) (*Config, string, error) {
	path, err := Resolve(explicitPath)
	if err != nil {
		return nil, "", err
	}

	cfg := &Config{}
	if path == "" {
		ApplyDefaults(cfg)
		return cfg, "", nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		// An explicit --config that does not exist yet is treated as a fresh
		// config to be created (e.g. by `project add`): return a defaulted empty
		// Config but keep the path so callers write back to it.
		if os.IsNotExist(err) {
			ApplyDefaults(cfg)
			return cfg, path, nil
		}
		return nil, path, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, path, fmt.Errorf("decode config %s: %w", path, err)
	}

	ApplyDefaults(cfg)
	if err := validate(cfg); err != nil {
		return nil, path, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, path, nil
}

// Resolve returns the absolute path of the config file to use, or "" when none
// of the candidate locations exist. An explicit path that does not exist is
// still returned (callers like `project add` may create it).
func Resolve(explicitPath string) (string, error) {
	if explicitPath != "" {
		return filepath.Abs(explicitPath)
	}
	if env := os.Getenv(EnvConfigPath); env != "" {
		return filepath.Abs(env)
	}

	for _, cand := range candidatePaths() {
		if cand == "" {
			continue
		}
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return filepath.Abs(cand)
		}
	}
	return "", nil
}

// ConfigDir returns the effective user-level config directory. When the env var
// GOFER_CONFIG_DIR is set it wins (absolute); otherwise the default is
// ~/.config/gofer. It holds both config.yaml and the global .env.
func ConfigDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv(EnvConfigDir)); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", DefaultConfigDirName), nil
}

// UserConfigPath returns the user-level default config path
// (<config-dir>/config.yaml; config-dir defaults to ~/.config/gofer).
func UserConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// WorkerConfigFileName is the conventional worker config filename, alongside the
// global config.yaml / .env in the config dir.
const WorkerConfigFileName = "worker.yaml"

// RuntimeFilePath returns the path of a runtime file <config-dir>/<sub>/<name>
// (e.g. RuntimeFilePath("run", "serve.pid") → <config-dir>/run/serve.pid). It is
// where daemon mode (-d) keeps pidfiles and redirected logs. When ConfigDir is
// unresolvable it degrades to ./<sub>/<name> so the process still has a usable
// path. Callers MkdirAll the parent dir before writing.
func RuntimeFilePath(sub, name string) string {
	dir, err := ConfigDir()
	if err != nil || dir == "" {
		return filepath.Join(sub, name)
	}
	return filepath.Join(dir, sub, name)
}

// UserWorkerConfigPath returns the user-level default worker config path
// (<config-dir>/worker.yaml; config-dir defaults to ~/.config/gofer). It is the
// fallback `gofer worker` uses when no --worker-config is given (mirrors how the
// server config falls back to <config-dir>/config.yaml).
func UserWorkerConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, WorkerConfigFileName), nil
}

// DBFileName is the SQLite metadata database file name used when db_path is
// resolved from storage.root or the config dir (see ResolveDBPath, design §11).
const DBFileName = "gofer.db"

// ResolveDBPath returns the SQLite metadata db path. Resolution order (design §11):
//  1. an explicit Storage.DBPath;
//  2. <Storage.Root>/gofer.db when Root is set;
//  3. <config-dir>/gofer.db otherwise.
//
// It is a single global db (one file per bridge process), independent of the
// per-project log result dirs.
func (c *Config) ResolveDBPath() string {
	if p := strings.TrimSpace(c.Storage.DBPath); p != "" {
		return p
	}
	if root := strings.TrimSpace(c.Storage.Root); root != "" {
		return filepath.Join(root, DBFileName)
	}
	// Fall back to the user config dir. ConfigDir only errors when the home dir
	// cannot be determined; degrade to a bare filename in CWD so the bridge still
	// has a usable (if non-ideal) path rather than failing to start.
	dir, err := ConfigDir()
	if err != nil || dir == "" {
		return DBFileName
	}
	return filepath.Join(dir, DBFileName)
}

// ResolveWorkerDBPath returns a ws-worker's SQLite metadata db path. It mirrors
// ResolveDBPath but is namespaced by workerID so a worker never collides on a single
// gofer.db when it shares a config dir with a serve (or with other workers — each
// gets its own id-named file). Resolution order:
//  1. an explicit Storage.DBPath;
//  2. <Storage.Root>/<workerID>.db when Root is set;
//  3. <config-dir>/worker/<workerID>.db otherwise.
//
// So both the bare default and an explicit Root=<config-dir>/worker converge on
// <config-dir>/worker/<workerID>.db. jobstore.Open MkdirAll's the parent dir.
func (c *Config) ResolveWorkerDBPath(workerID string) string {
	if p := strings.TrimSpace(c.Storage.DBPath); p != "" {
		return p
	}
	name := workerID + ".db"
	if root := strings.TrimSpace(c.Storage.Root); root != "" {
		return filepath.Join(root, name)
	}
	dir, err := ConfigDir()
	if err != nil || dir == "" {
		return filepath.Join("worker", name)
	}
	return filepath.Join(dir, "worker", name)
}

// candidatePaths lists the auto-discovery locations (current dir, then user).
func candidatePaths() []string {
	// Clone so appending the user path never mutates the package-level slice.
	out := append([]string{}, CurrentDirConfigNames...)
	if user, err := UserConfigPath(); err == nil {
		out = append(out, user)
	}
	return out
}

// ApplyDefaults fills storage defaults and initializes nil maps. It is exported
// so the ws-worker command can default a Config it builds from a WorkerConfig
// (so the worker's local store/registries behave like a serve process).
func ApplyDefaults(cfg *Config) {
	if cfg.Server.Addr == "" {
		cfg.Server.Addr = DefaultAddr
	}
	if cfg.Server.WebEnabled == nil {
		t := true
		cfg.Server.WebEnabled = &t
	}
	if cfg.Storage.DefaultExchangeSubdir == "" {
		cfg.Storage.DefaultExchangeSubdir = DefaultExchangeSubdir
	}
	if cfg.Storage.DefaultResultSubdir == "" {
		cfg.Storage.DefaultResultSubdir = DefaultResultSubdir
	}
	if cfg.Projects == nil {
		cfg.Projects = map[string]ProjectConfig{}
	}
	if cfg.Agents == nil {
		cfg.Agents = map[string]AgentConfig{}
	}
	if cfg.Runners == nil {
		cfg.Runners = map[string]RunnerConfig{}
	}
	if cfg.Roles == nil {
		cfg.Roles = map[string]RoleConfig{}
	}
}

// validate runs lightweight structural checks that do not touch the filesystem;
// path/agent existence checks live in internal/project Registry.Validate.
func validate(cfg *Config) error {
	for key, p := range cfg.Projects {
		if p.HostPath == "" {
			return fmt.Errorf("project %q: host_path is required", key)
		}
	}
	// E17 governance / per-caller quota sanity (design §7.1): negative values are a
	// config mistake (a negative cap/rate/burst has no meaning). 0 = unlimited is
	// fine; only reject < 0 so the legacy zero-everywhere config still validates.
	g := cfg.Server.Governance
	if g.DefaultCallerMaxConcurrent < 0 {
		return fmt.Errorf("server.governance.default_caller_max_concurrent must be >= 0")
	}
	if g.DefaultRateLimit < 0 {
		return fmt.Errorf("server.governance.default_rate_limit must be >= 0")
	}
	if g.DefaultRateBurst < 0 {
		return fmt.Errorf("server.governance.default_rate_burst must be >= 0")
	}
	for _, cc := range cfg.Server.Callers {
		if cc.MaxConcurrentJobs < 0 {
			return fmt.Errorf("caller %q: max_concurrent_jobs must be >= 0", cc.ID)
		}
		if cc.RateLimit < 0 {
			return fmt.Errorf("caller %q: rate_limit must be >= 0", cc.ID)
		}
		if cc.RateBurst < 0 {
			return fmt.Errorf("caller %q: rate_burst must be >= 0", cc.ID)
		}
	}
	// E25 supervisor auto-answer whitelist: every allow_prompt_regex must compile, so
	// a typo'd pattern fails fast here (serve start / `config validate`) instead of
	// being silently dropped at supervisor construction — where a missing pattern would
	// quietly widen escalation. An empty list stays escalate-only (the safe default).
	if cfg.Supervisor != nil {
		for i, p := range cfg.Supervisor.AllowPromptRegex {
			if _, err := regexp.Compile(p); err != nil {
				return fmt.Errorf("supervisor.allow_prompt_regex[%d] %q: %w", i, p, err)
			}
		}
		if cfg.Supervisor.IntervalSec < 0 {
			return fmt.Errorf("supervisor.interval_sec must be >= 0")
		}
		if cfg.Supervisor.MaxRoundsPerJob < 0 {
			return fmt.Errorf("supervisor.max_rounds_per_job must be >= 0")
		}
		// Event-driven reconciler (y5wt): desired_supervisors>0 needs a roles.supervisor
		// preset to source each on-demand sup job's agent/system_prompt/env (else every
		// dispatch would fail submit). reconcile_interval_sec<=0 just means "use default".
		if cfg.Supervisor.DesiredSupervisors < 0 {
			return fmt.Errorf("supervisor.desired_supervisors must be >= 0")
		}
		if cfg.Supervisor.DesiredSupervisors > 0 {
			rc, ok := cfg.Roles["supervisor"]
			if !ok {
				return fmt.Errorf("supervisor.desired_supervisors > 0 requires a roles.supervisor preset")
			}
			// The reconciler submits with no -p; the project must come from the preset
			// (resolveRole fills ProjectKey from RoleConfig.Project), else every
			// re-dispatch fails "unknown project".
			if rc.Project == "" {
				return fmt.Errorf("supervisor.desired_supervisors > 0 requires roles.supervisor.project (the reconciler submits with no project)")
			}
		}
		if cfg.Supervisor.ReconcileIntervalSec < 0 {
			return fmt.Errorf("supervisor.reconcile_interval_sec must be >= 0")
		}
		if cfg.Supervisor.ReconcileJobTimeoutSec < 0 {
			return fmt.Errorf("supervisor.reconcile_job_timeout_sec must be >= 0")
		}
	}
	// E36 presence TTLs are optional overrides in seconds; negative is a mistake
	// (0 = use the built-in default).
	if cfg.Presence.TTLSec < 0 || cfg.Presence.MessageTTLSec < 0 || cfg.Presence.PruneIntervalSec < 0 {
		return fmt.Errorf("presence ttl_sec/message_ttl_sec/prune_interval_sec must be >= 0")
	}
	return nil
}
