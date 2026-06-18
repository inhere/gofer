// Package config defines the dev-agent-bridge configuration model and the
// loader/writer that resolve, decode, default and persist it. See plan §6.1.
package config

import (
	"time"
)

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
	// DBPath is the optional explicit path to the SQLite metadata database. When
	// empty it is resolved by ResolveDBPath from Root / the config dir.
	DBPath string `yaml:"db_path"`
	// Retention bounds how many terminal jobs (and their logs) are kept; the
	// periodic prune in serve enforces it. Unset (all fields <= 0) disables prune.
	Retention RetentionConfig `yaml:"retention"`
}

// RetentionConfig is the YAML form of the job retention policy enforced by the
// serve prune loop (design §13 SP5). All fields default to 0 (disabled): with no
// retention configured the server never prunes (zero behaviour change).
type RetentionConfig struct {
	// MaxAgeDays, when > 0, prunes terminal jobs older than this many days.
	MaxAgeDays int `yaml:"max_age_days"`
	// MaxCount, when > 0, keeps only the newest MaxCount terminal jobs.
	MaxCount int `yaml:"max_count"`
	// IntervalMinutes is the prune cadence; <= 0 falls back to a default (60m) in
	// the serve loop. Only consulted when MaxAgeDays or MaxCount is > 0.
	IntervalMinutes int `yaml:"prune_interval_minutes"`
}

// Enabled reports whether any retention bound is set (so the serve prune loop
// should run). The interval alone does not enable prune.
func (r RetentionConfig) Enabled() bool { return r.MaxAgeDays > 0 || r.MaxCount > 0 }

// MaxAge converts MaxAgeDays into a time.Duration (0 days => 0, i.e. no age
// bound). The job package maps this onto a jobstore.RetentionPolicy — config
// stays free of a jobstore dependency so it remains a leaf imported everywhere.
func (r RetentionConfig) MaxAge() time.Duration {
	if r.MaxAgeDays > 0 {
		return time.Duration(r.MaxAgeDays) * 24 * time.Hour
	}
	return 0
}

// PruneInterval returns the prune cadence, defaulting to 60 minutes when the
// configured interval is <= 0.
func (r RetentionConfig) PruneInterval() time.Duration {
	if r.IntervalMinutes > 0 {
		return time.Duration(r.IntervalMinutes) * time.Minute
	}
	return 60 * time.Minute
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
