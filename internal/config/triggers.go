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
	Name    string `yaml:"name,omitempty"`
	Enabled bool   `yaml:"enabled,omitempty"`
	// Provider selects the inbound adapter: "generic" (default; HMAC + timestamp) or
	// "github" (GitHub-compatible webhooks), "dingtalk" or "feishu" (IM inbound adapters).
	// See TriggerProvider*.
	Provider    string              `yaml:"provider,omitempty"`
	OwnerCaller string              `yaml:"owner_caller,omitempty"`
	Project     string              `yaml:"project,omitempty"`
	Template    string              `yaml:"template,omitempty"`
	Auth        TriggerAuthConfig   `yaml:"auth,omitempty"`
	Input       TriggerInputConfig  `yaml:"input,omitempty"`
	Dedup       TriggerDedupConfig  `yaml:"dedup,omitempty"`
	Rate        TriggerRateConfig   `yaml:"rate,omitempty"`
	Limits      TriggerLimitsConfig `yaml:"limits,omitempty"`
	// GitHub holds the event filter of provider "github"; it must be empty otherwise.
	GitHub TriggerGitHubConfig `yaml:"github,omitempty"`
	// IM holds the message filter of the IM providers (dingtalk / feishu); it must be empty
	// for the other providers.
	IM TriggerIMConfig `yaml:"im,omitempty"`
}

// Provider names accepted by server.triggers[].provider.
const (
	TriggerProviderGeneric = "generic"
	TriggerProviderGitHub  = "github"
	// TriggerProviderDingTalk and TriggerProviderFeishu are the IM inbound adapters
	// (design P2): the adapter verifies the platform signature and normalizes the message
	// into an envelope; dispatch still goes through the shared trigger path.
	TriggerProviderDingTalk = "dingtalk"
	TriggerProviderFeishu   = "feishu"
)

// IsIM reports whether the provider is an IM inbound adapter.
func (t TriggerConfig) IsIM() bool {
	p := t.EffectiveProvider()
	return p == TriggerProviderDingTalk || p == TriggerProviderFeishu
}

// TriggerIMConfig narrows which IM messages dispatch. Everything is optional; an empty
// block dispatches every text message the platform delivers to the bot.
//
// CommandPrefix, when set, makes only messages that start with it (after @mentions and
// leading blanks are removed) dispatch; the prefix is removed from the text variable.
// AllowConversations / AllowSenders, when non-empty, are exact-match allowlists of the
// conversation id and the sender id; a message outside them is acknowledged and ignored.
type TriggerIMConfig struct {
	CommandPrefix      string   `yaml:"command_prefix,omitempty"`
	AllowConversations []string `yaml:"allow_conversations,omitempty"`
	AllowSenders       []string `yaml:"allow_senders,omitempty"`
}

func (c TriggerIMConfig) isZero() bool {
	return c.CommandPrefix == "" && len(c.AllowConversations) == 0 && len(c.AllowSenders) == 0
}

// IMVarFields are the whitelisted envelope facts an IM trigger may map to template
// variables (input.vars: name: im.<field>). Nothing else of the message can reach a prompt.
var IMVarFields = []string{"provider", "sender_id", "sender_name", "conversation_id", "text", "timestamp"}

func validIMVarField(f string) bool {
	for _, v := range IMVarFields {
		if v == f {
			return true
		}
	}
	return false
}

// MaxTriggerIMList bounds the IM allowlists; MaxTriggerIMValueBytes bounds one entry and
// the command prefix.
const (
	MaxTriggerIMList       = 256
	MaxTriggerIMValueBytes = 256
)

// EffectiveProvider is the provider with the "" default applied.
func (t TriggerConfig) EffectiveProvider() string {
	if t.Provider == "" {
		return TriggerProviderGeneric
	}
	return t.Provider
}

// TriggerGitHubConfig is the event filter of the github provider.
//
// Events lists what is dispatched; every other event (and the ping) is acknowledged with
// 200 "ignored" and starts nothing. An entry is an event name ("pull_request") or an event
// name plus action ("pull_request.opened"): the first matches every action of the event,
// the second only that action (the payload's top-level "action").
//
// Conditions narrow an entry down by one payload field. A condition applies to the
// deliveries its Event matches (same two forms as an events entry, and it must equal an
// entry of Events); all applying conditions must hold (AND), and a condition holds when the
// payload field Field (a dotted path of object keys and array indexes) is a string, number
// or boolean whose text equals one of Equals (OR). A missing field fails the condition.
// There is no other operator or expression.
//
// MaxAgeSec, when > 0, refuses (as stale) a delivery whose payload carries a recognizable
// time older than that many seconds; see the github adapter for the fields it reads. A
// payload without any of them is not refused.
type TriggerGitHubConfig struct {
	Events     []string                 `yaml:"events,omitempty"`
	Conditions []TriggerGitHubCondition `yaml:"conditions,omitempty"`
	MaxAgeSec  int                      `yaml:"max_age_sec,omitempty"`
}

type TriggerGitHubCondition struct {
	Event  string   `yaml:"event"`
	Field  string   `yaml:"field"`
	Equals []string `yaml:"equals"`
}

type TriggerAuthConfig struct {
	Kind            string `yaml:"kind,omitempty"`
	SecretEnv       string `yaml:"secret_env,omitempty"`
	TimestampHeader string `yaml:"timestamp_header,omitempty"`
	SignatureHeader string `yaml:"signature_header,omitempty"`
	ReplayWindowSec int    `yaml:"replay_window_sec,omitempty"`
	// VerificationTokenEnv names the environment variable that holds the platform's
	// verification token (feishu only: it authenticates the URL-verification handshake and
	// every event body).
	VerificationTokenEnv string `yaml:"verification_token_env,omitempty"`
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
	out.GitHub.Events = append([]string(nil), t.GitHub.Events...)
	out.GitHub.Conditions = make([]TriggerGitHubCondition, len(t.GitHub.Conditions))
	for i, c := range t.GitHub.Conditions {
		c.Equals = append([]string(nil), c.Equals...)
		out.GitHub.Conditions[i] = c
	}
	if len(t.GitHub.Events) == 0 {
		out.GitHub.Events = nil
	}
	if len(t.GitHub.Conditions) == 0 {
		out.GitHub.Conditions = nil
	}
	out.IM.AllowConversations = append([]string(nil), t.IM.AllowConversations...)
	out.IM.AllowSenders = append([]string(nil), t.IM.AllowSenders...)
	if len(t.IM.AllowConversations) == 0 {
		out.IM.AllowConversations = nil
	}
	if len(t.IM.AllowSenders) == 0 {
		out.IM.AllowSenders = nil
	}
	return out
}

var triggerNameRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._-]{0,63}$`)
var triggerEnvRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
var triggerRefRE = regexp.MustCompile(`^(body|message)\.[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$|^header\.[A-Za-z0-9-]+$`)

// triggerGitHubEventRE is an events / conditions entry: event, or event.action.
var triggerGitHubEventRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}(\.[a-z][a-z0-9_]{0,63})?$`)

// triggerGitHubFieldRE is a condition field: dotted object keys / array indexes.
var triggerGitHubFieldRE = regexp.MustCompile(`^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$`)

const (
	MaxTriggerGitHubEvents     = 64
	MaxTriggerGitHubConditions = 32
	MaxTriggerGitHubEquals     = 16
	MaxTriggerGitHubValueBytes = 256
)

// GitHubVarFields are the whitelisted payload facts a github trigger may map to template
// variables (input.vars: name: github.<field>). Nothing else of the payload can reach a prompt.
var GitHubVarFields = []string{"repository", "number", "title", "url", "sender", "branch", "base_branch", "sha", "event", "action", "conclusion"}

func validGitHubVarField(f string) bool {
	for _, v := range GitHubVarFields {
		if v == f {
			return true
		}
	}
	return false
}

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
			return fmt.Errorf("%s.owner_caller %q is not declared (owner_caller must name a caller declared in the configuration file)", where, owner)
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
		provider := t.Provider
		switch provider {
		case "", TriggerProviderGeneric, TriggerProviderGitHub, TriggerProviderDingTalk, TriggerProviderFeishu:
		default:
			return fmt.Errorf("%s.provider: unsupported %q (want generic, github, dingtalk or feishu)", where, provider)
		}
		if err := validateTriggerProvider(t, where); err != nil {
			return err
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
		if t.EffectiveProvider() == TriggerProviderGitHub && t.Dedup.Key != "" {
			return fmt.Errorf("%s.dedup.key is not allowed with provider github (X-GitHub-Delivery is always the dedup key)", where)
		}
		if t.IsIM() && t.Dedup.Key != "" {
			return fmt.Errorf("%s.dedup.key is not allowed with provider %s (the platform message / event id is always the dedup key)", where, t.EffectiveProvider())
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
			if t.EffectiveProvider() == TriggerProviderGitHub {
				field, ok := strings.CutPrefix(ref, "github.")
				if !ok || !validGitHubVarField(field) {
					return fmt.Errorf("%s.input.vars.%s: provider github accepts only github.<field> references (%s), got %q", where, name, strings.Join(GitHubVarFields, ", "), ref)
				}
				continue
			}
			if t.IsIM() {
				field, ok := strings.CutPrefix(ref, "im.")
				if !ok || !validIMVarField(field) {
					return fmt.Errorf("%s.input.vars.%s: provider %s accepts only im.<field> references (%s), got %q", where, name, t.EffectiveProvider(), strings.Join(IMVarFields, ", "), ref)
				}
				continue
			}
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

// validateTriggerProvider checks the provider-specific blocks: github needs its events and
// forbids the generic-only auth fields; generic forbids the github block.
func validateTriggerProvider(t *TriggerConfig, where string) error {
	if t.IsIM() {
		return validateTriggerIM(t, where)
	}
	if !t.IM.isZero() {
		return fmt.Errorf("%s.im is only valid with provider dingtalk or feishu", where)
	}
	if t.Auth.VerificationTokenEnv != "" {
		return fmt.Errorf("%s.auth.verification_token_env is only valid with provider feishu", where)
	}
	g := t.GitHub
	if t.EffectiveProvider() != TriggerProviderGitHub {
		if len(g.Events) > 0 || len(g.Conditions) > 0 || g.MaxAgeSec != 0 {
			return fmt.Errorf("%s.github is only valid with provider: github", where)
		}
		return nil
	}
	if t.Auth.TimestampHeader != "" || t.Auth.SignatureHeader != "" || t.Auth.ReplayWindowSec != 0 {
		return fmt.Errorf("%s.auth: timestamp_header, signature_header and replay_window_sec do not apply to provider github (X-Hub-Signature-256 is fixed; replay is stopped by X-GitHub-Delivery dedup, see github.max_age_sec)", where)
	}
	if len(g.Events) == 0 {
		return fmt.Errorf("%s.github.events is required (e.g. [\"pull_request.opened\"])", where)
	}
	if len(g.Events) > MaxTriggerGitHubEvents || len(g.Conditions) > MaxTriggerGitHubConditions {
		return fmt.Errorf("%s.github: too many events or conditions", where)
	}
	listed := make(map[string]struct{}, len(g.Events))
	for i, ev := range g.Events {
		if !triggerGitHubEventRE.MatchString(ev) {
			return fmt.Errorf("%s.github.events[%d]: invalid entry %q (want event or event.action)", where, i, ev)
		}
		listed[ev] = struct{}{}
	}
	for i, c := range g.Conditions {
		cw := fmt.Sprintf("%s.github.conditions[%d]", where, i)
		if _, ok := listed[c.Event]; !ok {
			return fmt.Errorf("%s.event %q is not one of github.events", cw, c.Event)
		}
		if !triggerGitHubFieldRE.MatchString(c.Field) {
			return fmt.Errorf("%s.field: invalid path %q", cw, c.Field)
		}
		if len(c.Equals) == 0 || len(c.Equals) > MaxTriggerGitHubEquals {
			return fmt.Errorf("%s.equals needs 1..%d values", cw, MaxTriggerGitHubEquals)
		}
		for _, v := range c.Equals {
			if len(v) > MaxTriggerGitHubValueBytes {
				return fmt.Errorf("%s.equals: value too long", cw)
			}
		}
	}
	if g.MaxAgeSec < 0 || g.MaxAgeSec > MaxTriggerDedupTTLSec {
		return fmt.Errorf("%s.github.max_age_sec out of range", where)
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
	// TriggerIssueVerificationTokenUnset: a feishu trigger's verification token variable is empty.
	TriggerIssueVerificationTokenUnset = "verification_token_env_unset"
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
	// Provider is the effective adapter name (generic / github / dingtalk / feishu).
	Provider string
	Enabled  bool
	Issues   []TriggerIssue
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
		st := TriggerStatus{Name: t.Name, Owner: strings.TrimSpace(t.OwnerCaller), Project: strings.TrimSpace(t.Project), Template: strings.TrimSpace(t.Template), Provider: t.EffectiveProvider(), Enabled: t.Enabled}
		if t.Enabled {
			if os.Getenv(t.Auth.SecretEnv) == "" {
				st.Issues = append(st.Issues, TriggerIssue{Code: TriggerIssueSecretEnvUnset, Arg: t.Auth.SecretEnv})
			}
			if t.Auth.VerificationTokenEnv != "" && os.Getenv(t.Auth.VerificationTokenEnv) == "" {
				st.Issues = append(st.Issues, TriggerIssue{Code: TriggerIssueVerificationTokenUnset, Arg: t.Auth.VerificationTokenEnv})
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

// validateTriggerIM checks an IM provider's blocks. The signature scheme of each platform is
// fixed, so the generic header overrides are refused rather than silently ignored.
func validateTriggerIM(t *TriggerConfig, where string) error {
	g := t.GitHub
	if len(g.Events) > 0 || len(g.Conditions) > 0 || g.MaxAgeSec != 0 {
		return fmt.Errorf("%s.github is only valid with provider: github", where)
	}
	if t.Auth.TimestampHeader != "" || t.Auth.SignatureHeader != "" {
		return fmt.Errorf("%s.auth: timestamp_header and signature_header do not apply to provider %s (the platform headers are fixed)", where, t.EffectiveProvider())
	}
	switch t.EffectiveProvider() {
	case TriggerProviderDingTalk:
		if t.Auth.VerificationTokenEnv != "" {
			return fmt.Errorf("%s.auth.verification_token_env is only valid with provider feishu", where)
		}
	case TriggerProviderFeishu:
		if !triggerEnvRE.MatchString(t.Auth.VerificationTokenEnv) {
			return fmt.Errorf("%s.auth.verification_token_env: provider feishu needs the environment variable name of the verification token", where)
		}
	}
	im := t.IM
	if len(im.CommandPrefix) > MaxTriggerIMValueBytes {
		return fmt.Errorf("%s.im.command_prefix is too long", where)
	}
	for field, list := range map[string][]string{"allow_conversations": im.AllowConversations, "allow_senders": im.AllowSenders} {
		if len(list) > MaxTriggerIMList {
			return fmt.Errorf("%s.im.%s: too many entries (max %d)", where, field, MaxTriggerIMList)
		}
		for _, v := range list {
			if v == "" || len(v) > MaxTriggerIMValueBytes {
				return fmt.Errorf("%s.im.%s: entries must be 1..%d bytes", where, field, MaxTriggerIMValueBytes)
			}
		}
	}
	return nil
}
