package config

import (
	"fmt"
	"os"
	"path/filepath"

	yaml "github.com/goccy/go-yaml"
)

// EnvConfigPath is the env var consulted in the config lookup chain (§6.1).
const EnvConfigPath = "AGENT_BRIDGE_CONFIG"

// CurrentDirConfigName is the per-directory config file name (§6.1).
const CurrentDirConfigName = ".dev-agent-bridge.yaml"

// Load resolves the config file via the lookup chain, decodes it into the
// strongly-typed Config, applies defaults and runs basic validation. It returns
// the loaded config and the resolved absolute path.
//
// Lookup order (§6.1):
//  1. explicitPath (CLI --config)
//  2. env AGENT_BRIDGE_CONFIG
//  3. ./.dev-agent-bridge.yaml
//  4. ~/.config/dev-agent-bridge/config.yaml
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
		applyDefaults(cfg)
		return cfg, "", nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		// An explicit --config that does not exist yet is treated as a fresh
		// config to be created (e.g. by `project add`): return a defaulted empty
		// Config but keep the path so callers write back to it.
		if os.IsNotExist(err) {
			applyDefaults(cfg)
			return cfg, path, nil
		}
		return nil, path, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, path, fmt.Errorf("decode config %s: %w", path, err)
	}

	applyDefaults(cfg)
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

// UserConfigPath returns the user-level default config path
// (~/.config/dev-agent-bridge/config.yaml).
func UserConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "dev-agent-bridge", "config.yaml"), nil
}

// candidatePaths lists the auto-discovery locations (current dir, then user).
func candidatePaths() []string {
	out := []string{CurrentDirConfigName}
	if user, err := UserConfigPath(); err == nil {
		out = append(out, user)
	}
	return out
}

// applyDefaults fills storage defaults and initializes nil maps.
func applyDefaults(cfg *Config) {
	if cfg.Server.Addr == "" {
		cfg.Server.Addr = DefaultAddr
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
	return nil
}
