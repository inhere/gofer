package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/inhere/gofer/internal/template"
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
	Vars map[string]string `yaml:"vars,omitempty"`
	// Message is NOT supported in P0: the IM message mapping belongs to the provider
	// adapters (design phase P2). The key is still decoded so validateTriggers can refuse
	// it — a field that parses but does nothing would read as a working mapping.
	Message string `yaml:"message,omitempty"`
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
		if t.Input.Message != "" {
			return fmt.Errorf("%s.input.message is not supported yet: map the message through input.vars (e.g. input.vars.text: body.message)", where)
		}
		if err := validateTriggerOwnerCeiling(cfg, t, owner, where); err != nil {
			return err
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

// validateTriggerOwnerCeiling refuses a trigger whose owner can never submit its fixed
// request, judged from the configuration file alone (so the verdict is the same on every
// machine and a reload that breaks it fails atomically): a read-only ceiling refuses every
// state-changing request, and a projects list that does not admit the fixed project would
// make every invocation fail at admission. Facts that depend on the environment (the
// secret variable, the template file) are NOT checked here; see TriggerStatuses.
func validateTriggerOwnerCeiling(cfg *Config, t *TriggerConfig, owner, where string) error {
	ceil, ok := cfg.Server.CallerCeiling(owner)
	if !ok {
		return nil
	}
	if ceil.IsReadOnly() {
		return fmt.Errorf("%s.owner_caller %q has a read_only ceiling and cannot submit jobs", where, owner)
	}
	if !ceil.AllowsProject(strings.TrimSpace(t.Project)) {
		return fmt.Errorf("%s.project %q is outside the ceiling of owner_caller %q", where, strings.TrimSpace(t.Project), owner)
	}
	return nil
}

// Machine-readable reasons a trigger is declared but cannot work on THIS machine.
const (
	TriggerIssueSecretEnvUnset   = "secret_env_unset"
	TriggerIssueTemplateNotFound = "template_not_found"
)

// TriggerIssue is one reason an enabled trigger is unavailable. Code is stable
// (TriggerIssue*); Arg is the value it is about (the variable or template name) — the CLI
// renders both through its localized text.
type TriggerIssue struct {
	Code string
	Arg  string
}

// TriggerStatus is the operator's view of one trigger: its fixed resources and, for an
// enabled one, why it cannot be used right now.
type TriggerStatus struct {
	Name, Owner, Project, Template string
	Enabled                        bool
	Issues                         []TriggerIssue
}

// Available reports whether an enabled trigger has no issue.
func (s TriggerStatus) Available() bool { return s.Enabled && len(s.Issues) == 0 }

// TriggerStatuses evaluates every trigger. Unlike validateTriggers it looks at the
// environment (the secret variable) and the file system (the task-book template), so it
// is a REPORT, not a load gate: a load that failed on a missing variable would break every
// CLI command run on a machine that does not hold the server's secrets (the same reason
// `config validate` only warns about unset job secrets). The design's "must not start" is
// honored where it matters at runtime: an unresolvable secret authenticates nothing (the
// engine refuses the request), and an unknown template fails the fixed-request preflight
// before any job exists.
func (c *Config) TriggerStatuses() []TriggerStatus {
	if c == nil {
		return nil
	}
	out := make([]TriggerStatus, 0, len(c.Server.Triggers))
	for _, t := range c.Server.Triggers {
		st := TriggerStatus{Name: t.Name, Owner: strings.TrimSpace(t.OwnerCaller), Project: strings.TrimSpace(t.Project), Template: strings.TrimSpace(t.Template), Enabled: t.Enabled}
		if t.Enabled {
			if os.Getenv(t.Auth.SecretEnv) == "" {
				st.Issues = append(st.Issues, TriggerIssue{Code: TriggerIssueSecretEnvUnset, Arg: t.Auth.SecretEnv})
			}
			if !c.triggerTemplateExists(st.Project, st.Template) {
				st.Issues = append(st.Issues, TriggerIssue{Code: TriggerIssueTemplateNotFound, Arg: st.Template})
			}
		}
		out = append(out, st)
	}
	return out
}

// triggerTemplateExists resolves the template the way the job layer does: the project's
// own directory first, then the global templates directory.
func (c *Config) triggerTemplateExists(project, name string) bool {
	globalDir, err := ConfigDir()
	if err != nil {
		globalDir = ""
	}
	projectDir := ""
	if proj, ok := c.Projects[project]; ok {
		if dir := c.ExecPath(proj); dir != "" {
			if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
				projectDir = dir
			}
		}
	}
	_, err = template.Resolve(name, projectDir, globalDir)
	return err == nil
}
