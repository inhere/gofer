package config

import (
	"strings"
	"testing"
)

func TestSecretsValidationAndJobVar(t *testing.T) {
	if got := (SecretConfig{Env: "X"}).JobVar("gh-token.ci"); got != "GH_TOKEN_CI" {
		t.Fatalf("default job var = %q", got)
	}
	if got := (SecretConfig{Env: "X", As: "MY_TOKEN"}).JobVar("gh"); got != "MY_TOKEN" {
		t.Fatalf("as = %q", got)
	}
	ok := &Config{Secrets: map[string]SecretConfig{"gh-token": {Env: "GITHUB_TOKEN"}},
		Projects: map[string]ProjectConfig{"p": {Secrets: []string{"gh-token"}}}}
	if err := ok.ValidateSecrets(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cfg  *Config
		want string
	}{
		{&Config{Secrets: map[string]SecretConfig{"Bad Name": {Env: "X"}}}, "invalid name"},
		{&Config{Secrets: map[string]SecretConfig{"a": {Env: ""}}}, "not a variable name"},
		{&Config{Secrets: map[string]SecretConfig{"a": {Env: "GOFER_SERVER_TOKEN"}}}, "gofer's own variables"},
		{&Config{Secrets: map[string]SecretConfig{"a": {Env: "X", As: "GOFER_JOB_TOKEN"}}}, "may not start with GOFER_"},
		{&Config{Secrets: map[string]SecretConfig{"a": {Env: "X", As: "1BAD"}}}, "not a usable variable name"},
		{&Config{Projects: map[string]ProjectConfig{"p": {Secrets: []string{"missing"}}}}, "not declared"},
	} {
		if err := tc.cfg.ValidateSecrets(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v, want %q", tc.cfg, err, tc.want)
		}
	}
}

func TestResolveSecretValue(t *testing.T) {
	cfg := &Config{Secrets: map[string]SecretConfig{"gh": {Env: "SECRET_RESOLVE_TEST_X", As: "GH_TOKEN"}}}
	t.Setenv("SECRET_RESOLVE_TEST_X", "value-1234")
	if v, val, err := cfg.ResolveSecretValue("gh"); err != nil || v != "GH_TOKEN" || val != "value-1234" {
		t.Fatalf("%q %q %v", v, val, err)
	}
	t.Setenv("SECRET_RESOLVE_TEST_X", "")
	if _, _, err := cfg.ResolveSecretValue("gh"); err == nil || !strings.Contains(err.Error(), "SECRET_RESOLVE_TEST_X is not set") || strings.Contains(err.Error(), "value-1234") {
		t.Fatalf("unset: %v", err)
	}
	if _, _, err := cfg.ResolveSecretValue("nope"); err == nil {
		t.Fatal("undeclared resolved")
	}
}
