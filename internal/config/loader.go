package config

import (
	"fmt"
	"os"
	"path/filepath"
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
	return nil
}
