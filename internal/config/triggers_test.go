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
