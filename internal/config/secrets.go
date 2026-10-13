package config

import (
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
)

// Job secrets (JOB-06②, docs/design/2026-10-13-job-secret-design.md): a job asks for
// a secret BY NAME (`job run --secret <name>`); the executing machine reads its value
// from its own process environment and hands it to the job under SecretConfig.As.
// The value is never in the config, the request or the job's logs. The registry and
// the per-project allowlist are FILE-ONLY: the config write API refuses them, or any
// caller who can edit config could map an arbitrary server variable to a secret.

// MinSecretValueLen is the shortest value a secret may have: output masking replaces
// the literal value, and a shorter one would mangle ordinary output.
const MinSecretValueLen = 4

var (
	secretNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	envVarPattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// SecretConfig is one secrets entry.
type SecretConfig struct {
	// Env is the variable of the executing gofer process holding the value (put it in
	// <config-dir>/.env to keep it out of the shell). Required.
	Env string `yaml:"env"`
	// As is the variable name the job sees ("" = the secret name upper-cased, with
	// '-' and '.' turned into '_').
	As string `yaml:"as,omitempty"`
	// Description is for people.
	Description string `yaml:"description,omitempty"`
}

// JobVar is the variable the job receives the secret as.
func (s SecretConfig) JobVar(name string) string {
	if strings.TrimSpace(s.As) != "" {
		return s.As
	}
	return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name))
}

// ValidateSecrets checks the registry and every project allowlist that names it.
func (c *Config) ValidateSecrets() error {
	for _, name := range slices.Sorted(maps.Keys(c.Secrets)) {
		s := c.Secrets[name]
		at := "secrets." + name
		if !secretNamePattern.MatchString(name) {
			return fmt.Errorf("%s: invalid name (lowercase letters, digits, '.', '_', '-'; max 64)", at)
		}
		if !envVarPattern.MatchString(s.Env) {
			return fmt.Errorf("%s.env %q is not a variable name", at, s.Env)
		}
		if goferOwnVar(s.Env) {
			return fmt.Errorf("%s.env %q: gofer's own variables cannot be handed to a job", at, s.Env)
		}
		if v := s.JobVar(name); !envVarPattern.MatchString(v) || goferOwnVar(v) {
			return fmt.Errorf("%s.as %q is not a usable variable name (and may not start with GOFER_)", at, v)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(c.Projects)) {
		for _, n := range c.Projects[key].Secrets {
			if _, ok := c.Secrets[n]; !ok {
				return fmt.Errorf("projects.%s.secrets: %q is not declared under secrets", key, n)
			}
		}
	}
	return nil
}

func goferOwnVar(name string) bool { return strings.HasPrefix(strings.ToUpper(name), "GOFER_") }

// ResolveSecretValue reads a secret's value from this process's environment. The
// error names the variable, never a value.
func (c *Config) ResolveSecretValue(name string) (jobVar, value string, err error) {
	s, ok := c.Secrets[name]
	if !ok {
		return "", "", fmt.Errorf("secret %q is not declared", name)
	}
	v, ok := os.LookupEnv(s.Env)
	if !ok || v == "" {
		return "", "", fmt.Errorf("secret %q: variable %s is not set on this machine", name, s.Env)
	}
	if len(v) < MinSecretValueLen {
		return "", "", fmt.Errorf("secret %q: the value of %s is shorter than %d characters (too short to mask in the job's output)", name, s.Env, MinSecretValueLen)
	}
	return s.JobVar(name), v, nil
}

func cloneSecrets(in map[string]SecretConfig) map[string]SecretConfig {
	if in == nil {
		return nil
	}
	return maps.Clone(in)
}
