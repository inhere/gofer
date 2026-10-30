package config

import "testing"

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
