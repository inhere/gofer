// Package config defines the dev-agent-bridge configuration model and the
// loader/writer that resolve, decode, default and persist it. See plan §6.1.
package config

// Default values used during config loading. See plan §6.1.
const (
	DefaultAddr           = "0.0.0.0:8765"
	DefaultExchangeSubdir = "tmp"
	DefaultResultSubdir   = "dev-agent-bridge"
)

// Config is the top-level dev-agent-bridge configuration. Unknown top-level
// keys present in the source file are preserved on write (see writer.go).
type Config struct {
	Server   ServerConfig             `yaml:"server"`
	Storage  StorageConfig            `yaml:"storage"`
	Projects map[string]ProjectConfig `yaml:"projects"`
	Agents   map[string]AgentConfig   `yaml:"agents"`
	Runners  map[string]RunnerConfig  `yaml:"runners"`
}

// ServerConfig holds HTTP server and auth settings.
type ServerConfig struct {
	Addr            string `yaml:"addr"`
	Token           string `yaml:"token"`
	TokenEnv        string `yaml:"token_env"`
	AllowEmptyToken bool   `yaml:"allow_empty_token"`
	// WebEnabled is a pointer so that "unset" (nil) can default to true while an
	// explicit web_enabled:false disables the embedded web console (see
	// IsWebEnabled and applyDefaults).
	WebEnabled *bool `yaml:"web_enabled"`
}

// IsWebEnabled reports whether the web console should be mounted. Unset (nil)
// defaults to true; an explicit web_enabled:false disables it.
func (sc ServerConfig) IsWebEnabled() bool { return sc.WebEnabled == nil || *sc.WebEnabled }

// StorageConfig holds defaults for the per-project exchange/result subdirs and
// an optional global store root. When Root is empty (default), each project
// stores results under its own exchange subdir; when Root is set it becomes a
// global store keyed by project (see ResultBaseDir in internal/project).
type StorageConfig struct {
	DefaultExchangeSubdir string `yaml:"default_exchange_subdir"`
	DefaultResultSubdir   string `yaml:"default_result_subdir"`
	Root                  string `yaml:"root"`
}

// ProjectConfig describes a single registered project. ExchangeSubdir and
// ResultSubdir may be empty; they fall back to the storage defaults at resolve
// time (see ResolvedExchangeSubdir/ResolvedResultSubdir).
type ProjectConfig struct {
	HostPath          string   `yaml:"host_path"`
	ContainerPath     string   `yaml:"container_path"`
	ExchangeSubdir    string   `yaml:"exchange_subdir"`
	ResultSubdir      string   `yaml:"result_subdir"`
	DefaultAgent      string   `yaml:"default_agent"`
	AllowedAgents     []string `yaml:"allowed_agents"`
	AllowedRunners    []string `yaml:"allowed_runners"`
	AllowExec         bool     `yaml:"allow_exec"`
	MaxConcurrentJobs int      `yaml:"max_concurrent_jobs"`
}

// AgentConfig describes a configurable CLI agent. Detect is refined in P3; P2
// only needs it to decode cleanly.
type AgentConfig struct {
	Type        string            `yaml:"type"`
	Command     string            `yaml:"command"`
	Args        []string          `yaml:"args"`
	Env         map[string]string `yaml:"env"`
	AllowRawCmd bool              `yaml:"allow_raw_cmd"`
	Detect      DetectConfig      `yaml:"detect"`
}

// DetectConfig is the agent availability probe. Placeholder in P2, refined P3.
type DetectConfig struct {
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
}

// RunnerConfig describes an execution location. peer-http fields are decoded in
// P2 but only used by the peer runner in P7.
type RunnerConfig struct {
	Type     string `yaml:"type"`
	BaseURL  string `yaml:"base_url"`
	TokenEnv string `yaml:"token_env"`
}

// ProjectAllowedAgents returns the allowed_agents list for projectKey. The
// second return is false when the project is not registered. Used by the agent
// package to enforce the per-project allowlist (plan §11).
func (c *Config) ProjectAllowedAgents(projectKey string) ([]string, bool) {
	p, ok := c.Projects[projectKey]
	if !ok {
		return nil, false
	}
	return p.AllowedAgents, true
}

// ResolvedExchangeSubdir returns the effective exchange subdir for a project,
// falling back to the storage default (or the hard default) when unset.
func (c *Config) ResolvedExchangeSubdir(p ProjectConfig) string {
	if p.ExchangeSubdir != "" {
		return p.ExchangeSubdir
	}
	if c.Storage.DefaultExchangeSubdir != "" {
		return c.Storage.DefaultExchangeSubdir
	}
	return DefaultExchangeSubdir
}

// ResolvedResultSubdir returns the effective result subdir for a project,
// falling back to the storage default (or the hard default) when unset.
func (c *Config) ResolvedResultSubdir(p ProjectConfig) string {
	if p.ResultSubdir != "" {
		return p.ResultSubdir
	}
	if c.Storage.DefaultResultSubdir != "" {
		return c.Storage.DefaultResultSubdir
	}
	return DefaultResultSubdir
}
