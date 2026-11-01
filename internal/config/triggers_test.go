package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateTriggersOwnerRules(t *testing.T) {
	base := func(owner string) *Config {
		return &Config{Server: ServerConfig{Callers: []CallerConfig{{ID: "automation", Token: "x"}, {ID: "root", CanAdmin: true}}, Triggers: []TriggerConfig{{Name: "push", OwnerCaller: owner, Project: "demo", Template: "source-update", Auth: TriggerAuthConfig{SecretEnv: "GOFER_TRIGGER_PUSH"}}}}}
	}
	if err := validateTriggers(base("automation")); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"root", "missing"} {
		if err := validateTriggers(base(owner)); err == nil {
			t.Fatalf("owner %q accepted", owner)
		}
	}
	legacy := &Config{Server: ServerConfig{Triggers: []TriggerConfig{{Name: "push", OwnerCaller: "default", Project: "demo", Template: "t", Auth: TriggerAuthConfig{SecretEnv: "GOFER_TRIGGER_PUSH"}}}}}
	if err := validateTriggers(legacy); err == nil {
		t.Fatal("implicit legacy root accepted")
	}
}

func TestValidateTriggersInputReferenceAndDefaults(t *testing.T) {
	cfg := &Config{Server: ServerConfig{Callers: []CallerConfig{{ID: "automation"}}, Triggers: []TriggerConfig{{Name: "push", OwnerCaller: "automation", Project: "demo", Template: "t", Auth: TriggerAuthConfig{SecretEnv: "GOFER_TRIGGER_PUSH"}, Input: TriggerInputConfig{Vars: map[string]string{"ref": "body.ref"}}}}}}
	if err := validateTriggers(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Triggers[0].Auth.ReplayWindowSec != 0 {
		t.Fatal("Validate mutated trigger defaults")
	}
	cfg.Server.Triggers[0].Input.Vars["bad"] = "body..ref"
	if err := validateTriggers(cfg); err == nil {
		t.Fatal("invalid reference accepted")
	}
}

func validTrigger(mut func(*TriggerConfig)) *Config {
	tc := TriggerConfig{Name: "push", Enabled: true, OwnerCaller: "automation", Project: "demo", Template: "t", Auth: TriggerAuthConfig{SecretEnv: "GOFER_TRIGGER_PUSH"}}
	if mut != nil {
		mut(&tc)
	}
	return &Config{Server: ServerConfig{Callers: []CallerConfig{{ID: "automation"}}, Triggers: []TriggerConfig{tc}}}
}

func TestValidateTriggersRejections(t *testing.T) {
	cases := map[string]func(*TriggerConfig){
		"bad name":              func(tc *TriggerConfig) { tc.Name = "1bad name" },
		"secret_env lowercase":  func(tc *TriggerConfig) { tc.Auth.SecretEnv = "lower_case" },
		"secret_env empty":      func(tc *TriggerConfig) { tc.Auth.SecretEnv = "" },
		"secret_env with dash":  func(tc *TriggerConfig) { tc.Auth.SecretEnv = "A-B" },
		"replay window over":    func(tc *TriggerConfig) { tc.Auth.ReplayWindowSec = MaxTriggerReplayWindowSec + 1 },
		"replay window neg":     func(tc *TriggerConfig) { tc.Auth.ReplayWindowSec = -1 },
		"ttl over":              func(tc *TriggerConfig) { tc.Dedup.TTLSec = MaxTriggerDedupTTLSec + 1 },
		"body over":             func(tc *TriggerConfig) { tc.Limits.MaxBodyBytes = MaxTriggerBodyBytes + 1 },
		"var over":              func(tc *TriggerConfig) { tc.Limits.MaxVarBytes = MaxTriggerVarBytes + 1 },
		"negative rate":         func(tc *TriggerConfig) { tc.Rate.PerMinute = -1 },
		"unsupported auth kind": func(tc *TriggerConfig) { tc.Auth.Kind = "bearer" },
		"bad dedup key":         func(tc *TriggerConfig) { tc.Dedup.Key = "body..x" },
		"no project":            func(tc *TriggerConfig) { tc.Project = " " },
		"no template":           func(tc *TriggerConfig) { tc.Template = "" },
		"input.message":         func(tc *TriggerConfig) { tc.Input.Message = "body.message" },
	}
	for name, mut := range cases {
		if err := validateTriggers(validTrigger(mut)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The bounds themselves are allowed.
	ok := validTrigger(func(tc *TriggerConfig) {
		tc.Auth.ReplayWindowSec = MaxTriggerReplayWindowSec
		tc.Dedup.TTLSec = MaxTriggerDedupTTLSec
		tc.Limits = TriggerLimitsConfig{MaxBodyBytes: MaxTriggerBodyBytes, MaxVarBytes: MaxTriggerVarBytes}
	})
	if err := validateTriggers(ok); err != nil {
		t.Fatalf("limits at the bound: %v", err)
	}
}

func TestValidateTriggersDuplicateName(t *testing.T) {
	cfg := validTrigger(nil)
	cfg.Server.Triggers = append(cfg.Server.Triggers, cfg.Server.Triggers[0])
	if err := validateTriggers(cfg); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateTriggersOwnerCeiling(t *testing.T) {
	yes := true
	readOnly := validTrigger(nil)
	readOnly.Server.Callers[0].Ceiling = &CallerCeiling{ReadOnly: &yes}
	if err := validateTriggers(readOnly); err == nil || !strings.Contains(err.Error(), "read_only") {
		t.Fatalf("read-only owner accepted: %v", err)
	}
	wrongProject := validTrigger(nil)
	wrongProject.Server.Callers[0].Ceiling = &CallerCeiling{Projects: []string{"other"}}
	if err := validateTriggers(wrongProject); err == nil || !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("outside-ceiling project accepted: %v", err)
	}
	fine := validTrigger(nil)
	fine.Server.Callers[0].Ceiling = &CallerCeiling{Projects: []string{"dem*"}}
	if err := validateTriggers(fine); err != nil {
		t.Fatalf("in-ceiling project: %v", err)
	}
}

func TestTriggerStatuses(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv(EnvConfigDir, cfgDir)
	t.Setenv("GOFER_TRIGGER_PUSH", "")
	cfg := validTrigger(nil)
	st := cfg.TriggerStatuses()
	if len(st) != 1 || st[0].Available() || len(st[0].Issues) != 2 || st[0].Owner != "automation" || st[0].Project != "demo" || st[0].Template != "t" {
		t.Fatalf("status=%+v", st)
	}
	codes := []string{st[0].Issues[0].Code, st[0].Issues[1].Code}
	if codes[0] != TriggerIssueSecretEnvUnset || codes[1] != TriggerIssueTemplateNotFound {
		t.Fatalf("codes=%v", codes)
	}

	t.Setenv("GOFER_TRIGGER_PUSH", "s")
	if err := os.MkdirAll(filepath.Join(cfgDir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "templates", "t.md"), []byte("body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := cfg.TriggerStatuses(); !st[0].Available() {
		t.Fatalf("status=%+v", st)
	}

	off := validTrigger(func(tc *TriggerConfig) { tc.Enabled = false })
	if st := off.TriggerStatuses(); st[0].Enabled || st[0].Available() || len(st[0].Issues) != 0 {
		t.Fatalf("disabled status=%+v", st)
	}
}

func ghTrigger(mut func(*TriggerConfig)) *Config {
	return validTrigger(func(tc *TriggerConfig) {
		tc.Provider = TriggerProviderGitHub
		tc.GitHub = TriggerGitHubConfig{
			Events:     []string{"pull_request.opened", "check_run.completed", "push"},
			Conditions: []TriggerGitHubCondition{{Event: "check_run.completed", Field: "check_run.conclusion", Equals: []string{"failure"}}},
			MaxAgeSec:  3600,
		}
		tc.Input.Vars = map[string]string{"repo": "github.repository", "n": "github.number"}
		if mut != nil {
			mut(tc)
		}
	})
}

func TestValidateTriggersProvider(t *testing.T) {
	if err := validateTriggers(ghTrigger(nil)); err != nil {
		t.Fatalf("valid github trigger: %v", err)
	}
	if err := validateTriggers(validTrigger(func(tc *TriggerConfig) { tc.Provider = "generic" })); err != nil {
		t.Fatalf("explicit generic: %v", err)
	}
	if got := validTrigger(nil).Server.Triggers[0].EffectiveProvider(); got != TriggerProviderGeneric {
		t.Fatalf("default provider = %q", got)
	}
	// An unknown provider names the offending key.
	err := validateTriggers(validTrigger(func(tc *TriggerConfig) { tc.Provider = "gitlab" }))
	if err == nil || !strings.Contains(err.Error(), "server.triggers[0].provider") || !strings.Contains(err.Error(), "gitlab") {
		t.Fatalf("unknown provider err = %v", err)
	}
	cases := map[string]func(*TriggerConfig){
		"no events":                   func(tc *TriggerConfig) { tc.GitHub.Events = nil },
		"bad event entry":             func(tc *TriggerConfig) { tc.GitHub.Events = []string{"Pull Request"} },
		"too deep event":              func(tc *TriggerConfig) { tc.GitHub.Events = []string{"a.b.c"} },
		"condition on unlisted event": func(tc *TriggerConfig) { tc.GitHub.Conditions[0].Event = "issues.opened" },
		"condition bad field":         func(tc *TriggerConfig) { tc.GitHub.Conditions[0].Field = "a..b" },
		"condition no values":         func(tc *TriggerConfig) { tc.GitHub.Conditions[0].Equals = nil },
		"negative max age":            func(tc *TriggerConfig) { tc.GitHub.MaxAgeSec = -1 },
		"max age over":                func(tc *TriggerConfig) { tc.GitHub.MaxAgeSec = MaxTriggerDedupTTLSec + 1 },
		"dedup key":                   func(tc *TriggerConfig) { tc.Dedup.Key = "header.X-GitHub-Delivery" },
		"replay window":               func(tc *TriggerConfig) { tc.Auth.ReplayWindowSec = 60 },
		"signature header":            func(tc *TriggerConfig) { tc.Auth.SignatureHeader = "X-Sig" },
		"timestamp header":            func(tc *TriggerConfig) { tc.Auth.TimestampHeader = "X-Ts" },
		"body reference":              func(tc *TriggerConfig) { tc.Input.Vars = map[string]string{"x": "body.pull_request.body"} },
		"header reference":            func(tc *TriggerConfig) { tc.Input.Vars = map[string]string{"x": "header.X-Anything"} },
		"unknown github field":        func(tc *TriggerConfig) { tc.Input.Vars = map[string]string{"x": "github.body"} },
		"no secret env":               func(tc *TriggerConfig) { tc.Auth.SecretEnv = "" },
	}
	for name, mut := range cases {
		if err := validateTriggers(ghTrigger(mut)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The github block does not belong to a generic trigger.
	if err := validateTriggers(validTrigger(func(tc *TriggerConfig) { tc.GitHub.Events = []string{"push"} })); err == nil || !strings.Contains(err.Error(), "provider: github") {
		t.Fatalf("github block on generic trigger: %v", err)
	}
	// Every documented variable is accepted.
	all := ghTrigger(func(tc *TriggerConfig) {
		tc.Input.Vars = map[string]string{}
		for _, f := range GitHubVarFields {
			tc.Input.Vars[f] = "github." + f
		}
	})
	if err := validateTriggers(all); err != nil {
		t.Fatalf("whitelist fields: %v", err)
	}
}

func TestTriggerGitHubCloneAndStatusProvider(t *testing.T) {
	cfg := ghTrigger(nil)
	cp := cfg.Server.Triggers[0].Clone()
	cp.GitHub.Events[0] = "mutated"
	cp.GitHub.Conditions[0].Equals[0] = "mutated"
	if cfg.Server.Triggers[0].GitHub.Events[0] == "mutated" || cfg.Server.Triggers[0].GitHub.Conditions[0].Equals[0] == "mutated" {
		t.Fatal("Clone shares the github filter with the original")
	}
	if st := cfg.TriggerStatuses(); st[0].Provider != "github" {
		t.Fatalf("status provider = %q", st[0].Provider)
	}
	if st := validTrigger(nil).TriggerStatuses(); st[0].Provider != "generic" {
		t.Fatalf("generic status provider = %q", st[0].Provider)
	}
}

func imTrigger(provider string, mut func(*TriggerConfig)) *Config {
	return validTrigger(func(tc *TriggerConfig) {
		tc.Provider = provider
		tc.Input.Vars = map[string]string{"who": "im.sender_name", "text": "im.text"}
		tc.IM = TriggerIMConfig{CommandPrefix: "/run", AllowConversations: []string{"room-1"}}
		if provider == TriggerProviderFeishu {
			tc.Auth.VerificationTokenEnv = "GOFER_TRIGGER_PUSH_TOKEN"
		}
		if mut != nil {
			mut(tc)
		}
	})
}

func TestValidateTriggersIMProviders(t *testing.T) {
	for _, p := range []string{TriggerProviderDingTalk, TriggerProviderFeishu} {
		if err := validateTriggers(imTrigger(p, nil)); err != nil {
			t.Fatalf("valid %s trigger: %v", p, err)
		}
		all := imTrigger(p, func(tc *TriggerConfig) {
			tc.Input.Vars = map[string]string{}
			for _, f := range IMVarFields {
				tc.Input.Vars[f] = "im." + f
			}
		})
		if err := validateTriggers(all); err != nil {
			t.Fatalf("%s: every whitelisted field must be accepted: %v", p, err)
		}
		bad := map[string]func(*TriggerConfig){
			"body reference":        func(tc *TriggerConfig) { tc.Input.Vars = map[string]string{"x": "body.sessionWebhook"} },
			"unknown im field":      func(tc *TriggerConfig) { tc.Input.Vars = map[string]string{"x": "im.raw"} },
			"header override":       func(tc *TriggerConfig) { tc.Auth.SignatureHeader = "X-Sig" },
			"dedup key":             func(tc *TriggerConfig) { tc.Dedup.Key = "body.msgId" },
			"github block":          func(tc *TriggerConfig) { tc.GitHub.Events = []string{"push"} },
			"empty allowlist entry": func(tc *TriggerConfig) { tc.IM.AllowSenders = []string{""} },
			"long prefix":           func(tc *TriggerConfig) { tc.IM.CommandPrefix = strings.Repeat("p", MaxTriggerIMValueBytes+1) },
			"bad secret env":        func(tc *TriggerConfig) { tc.Auth.SecretEnv = "lower" },
		}
		for name, mut := range bad {
			if err := validateTriggers(imTrigger(p, mut)); err == nil {
				t.Errorf("%s: %s must be rejected", p, name)
			}
		}
	}
	// feishu needs its verification token variable; dingtalk and the others refuse it
	if err := validateTriggers(imTrigger(TriggerProviderFeishu, func(tc *TriggerConfig) { tc.Auth.VerificationTokenEnv = "" })); err == nil || !strings.Contains(err.Error(), "verification_token_env") {
		t.Fatalf("feishu without token env: %v", err)
	}
	if err := validateTriggers(imTrigger(TriggerProviderDingTalk, func(tc *TriggerConfig) { tc.Auth.VerificationTokenEnv = "GOFER_X" })); err == nil {
		t.Fatal("dingtalk with verification_token_env must be rejected")
	}
	// the im block and the token belong to IM providers only
	if err := validateTriggers(validTrigger(func(tc *TriggerConfig) { tc.IM.CommandPrefix = "/run" })); err == nil || !strings.Contains(err.Error(), "im is only valid") {
		t.Fatalf("im block on generic trigger: %v", err)
	}
	if err := validateTriggers(ghTrigger(func(tc *TriggerConfig) { tc.IM.AllowSenders = []string{"a"} })); err == nil {
		t.Fatal("im block on github trigger must be rejected")
	}
	if err := validateTriggers(validTrigger(func(tc *TriggerConfig) { tc.Auth.VerificationTokenEnv = "GOFER_X" })); err == nil {
		t.Fatal("verification_token_env on generic trigger must be rejected")
	}
	// im.<field> is not a generic reference
	if err := validateTriggers(validTrigger(func(tc *TriggerConfig) { tc.Input.Vars = map[string]string{"x": "im.text"} })); err == nil {
		t.Fatal("im.text on a generic trigger must be rejected")
	}
}

func TestTriggerIMCloneAndStatuses(t *testing.T) {
	t.Setenv("GOFER_TRIGGER_PUSH", "s")
	t.Setenv("GOFER_TRIGGER_PUSH_TOKEN", "")
	cfg := imTrigger(TriggerProviderFeishu, nil)
	clone := cfg.Server.Triggers[0].Clone()
	clone.IM.AllowConversations[0] = "changed"
	if cfg.Server.Triggers[0].IM.AllowConversations[0] != "room-1" {
		t.Fatal("Clone shares the im allowlist")
	}
	st := cfg.TriggerStatuses()
	if st[0].Provider != "feishu" {
		t.Fatalf("provider = %q", st[0].Provider)
	}
	found := false
	for _, is := range st[0].Issues {
		found = found || (is.Code == TriggerIssueVerificationTokenUnset && is.Arg == "GOFER_TRIGGER_PUSH_TOKEN")
	}
	if !found {
		t.Fatalf("issues = %+v, want the unset verification token", st[0].Issues)
	}
}
