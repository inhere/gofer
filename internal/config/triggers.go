package config

import (
	"fmt"
	"regexp"
	"strings"
)

// TriggerConfig is a file-only inbound dispatch definition. An enabled trigger
// can create jobs, so it is intentionally not writable through the API.
type TriggerConfig struct {
	Name        string              `yaml:"name,omitempty"`
	Enabled     bool                `yaml:"enabled,omitempty"`
	OwnerCaller string              `yaml:"owner_caller,omitempty"`
	Project     string              `yaml:"project,omitempty"`
	Template    string              `yaml:"template,omitempty"`
	Auth        TriggerAuthConfig   `yaml:"auth,omitempty"`
	Input       TriggerInputConfig  `yaml:"input,omitempty"`
	Dedup       TriggerDedupConfig  `yaml:"dedup,omitempty"`
	Rate        TriggerRateConfig   `yaml:"rate,omitempty"`
	Limits      TriggerLimitsConfig `yaml:"limits,omitempty"`
}

type TriggerAuthConfig struct {
	Kind            string `yaml:"kind,omitempty"`
	SecretEnv       string `yaml:"secret_env,omitempty"`
	TimestampHeader string `yaml:"timestamp_header,omitempty"`
	SignatureHeader string `yaml:"signature_header,omitempty"`
	ReplayWindowSec int    `yaml:"replay_window_sec,omitempty"`
}

type TriggerInputConfig struct {
	Vars    map[string]string `yaml:"vars,omitempty"`
	Message string            `yaml:"message,omitempty"`
}

type TriggerDedupConfig struct {
	Key    string `yaml:"key,omitempty"`
	TTLSec int    `yaml:"ttl_sec,omitempty"`
}

type TriggerRateConfig struct {
	PerMinute   int `yaml:"per_minute,omitempty"`
	Burst       int `yaml:"burst,omitempty"`
	MaxInflight int `yaml:"max_inflight,omitempty"`
}

type TriggerLimitsConfig struct {
	MaxBodyBytes int `yaml:"max_body_bytes,omitempty"`
	MaxVarBytes  int `yaml:"max_var_bytes,omitempty"`
}

func (t TriggerConfig) Clone() TriggerConfig {
	out := t
	if t.Input.Vars != nil {
		out.Input.Vars = make(map[string]string, len(t.Input.Vars))
		for k, v := range t.Input.Vars {
			out.Input.Vars[k] = v
		}
	}
	return out
}

var triggerNameRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._-]{0,63}$`)
var triggerEnvRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
var triggerRefRE = regexp.MustCompile(`^(body|message)\.[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$|^header\.[A-Za-z0-9-]+$`)

const (
	DefaultTriggerReplayWindowSec = 300
	DefaultTriggerDedupTTLSec     = 86400
	DefaultTriggerMaxBodyBytes    = 262144
	DefaultTriggerMaxVarBytes     = 32768
	MaxTriggerReplayWindowSec     = 3600
	MaxTriggerDedupTTLSec         = 7 * 86400
	MaxTriggerBodyBytes           = 4 << 20
	MaxTriggerVarBytes            = 1 << 20
)

func validateTriggers(cfg *Config) error {
	seen := make(map[string]struct{}, len(cfg.Server.Triggers))
	for i := range cfg.Server.Triggers {
		t := &cfg.Server.Triggers[i]
		where := fmt.Sprintf("server.triggers[%d]", i)
		if !triggerNameRE.MatchString(t.Name) {
			return fmt.Errorf("%s.name: invalid trigger name %q", where, t.Name)
		}
		if _, ok := seen[t.Name]; ok {
			return fmt.Errorf("server.triggers: duplicate name %q", t.Name)
		}
		seen[t.Name] = struct{}{}
		if strings.TrimSpace(t.OwnerCaller) == "" {
			return fmt.Errorf("%s.owner_caller is required", where)
		}
		owner := strings.TrimSpace(t.OwnerCaller)
		found, root := false, false
		for _, c := range cfg.Server.Callers {
			if c.ID == owner {
				found = true
				root = c.CanAdmin
				break
			}
		}
		if !found && len(cfg.Server.Callers) == 0 && owner == "default" && !cfg.Server.AllowEmptyToken {
			found, root = true, true
		}
		if !found {
			return fmt.Errorf("%s.owner_caller %q is not declared", where, owner)
		}
		if root {
			return fmt.Errorf("%s.owner_caller %q must not be root", where, owner)
		}
		if strings.TrimSpace(t.Project) == "" {
			return fmt.Errorf("%s.project is required", where)
		}
		if strings.TrimSpace(t.Template) == "" {
			return fmt.Errorf("%s.template is required", where)
		}
		kind := t.Auth.Kind
		if kind == "" {
			kind = "hmac-sha256"
		}
		if kind != "hmac-sha256" {
			return fmt.Errorf("%s.auth.kind: unsupported %q", where, kind)
		}
		if !triggerEnvRE.MatchString(t.Auth.SecretEnv) {
			return fmt.Errorf("%s.auth.secret_env: invalid environment variable name", where)
		}
		replayWindow := t.Auth.ReplayWindowSec
		if replayWindow == 0 {
			replayWindow = DefaultTriggerReplayWindowSec
		}
		if replayWindow < 1 || replayWindow > MaxTriggerReplayWindowSec {
			return fmt.Errorf("%s.auth.replay_window_sec out of range", where)
		}
		if t.Dedup.Key != "" && t.Dedup.Key != "Idempotency-Key" && !triggerRefRE.MatchString(t.Dedup.Key) {
			return fmt.Errorf("%s.dedup.key: invalid reference %q", where, t.Dedup.Key)
		}
		ttl := t.Dedup.TTLSec
		if ttl == 0 {
			ttl = DefaultTriggerDedupTTLSec
		}
		if ttl < 1 || ttl > MaxTriggerDedupTTLSec {
			return fmt.Errorf("%s.dedup.ttl_sec out of range", where)
		}
		if t.Rate.PerMinute < 0 || t.Rate.Burst < 0 || t.Rate.MaxInflight < 0 {
			return fmt.Errorf("%s.rate values must be >= 0", where)
		}
		maxBody := t.Limits.MaxBodyBytes
		if maxBody == 0 {
			maxBody = DefaultTriggerMaxBodyBytes
		}
		maxVar := t.Limits.MaxVarBytes
		if maxVar == 0 {
			maxVar = DefaultTriggerMaxVarBytes
		}
		if maxBody < 1 || maxBody > MaxTriggerBodyBytes || maxVar < 1 || maxVar > MaxTriggerVarBytes {
			return fmt.Errorf("%s.limits exceed server bounds", where)
		}
		for name, ref := range t.Input.Vars {
			if !triggerRefRE.MatchString(ref) {
				return fmt.Errorf("%s.input.vars.%s: invalid reference %q", where, name, ref)
			}
		}
		if t.Input.Message != "" && !triggerRefRE.MatchString(t.Input.Message) {
			return fmt.Errorf("%s.input.message: invalid reference %q", where, t.Input.Message)
		}
	}
	return nil
}

// Triggers returns a defensive copy of the server trigger definitions.
func (c *Config) Triggers() []TriggerConfig {
	if c == nil {
		return nil
	}
	out := make([]TriggerConfig, len(c.Server.Triggers))
	for i := range c.Server.Triggers {
		out[i] = c.Server.Triggers[i].Clone()
	}
	return out
}
