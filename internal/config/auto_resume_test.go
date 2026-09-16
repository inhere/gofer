package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutoResumeConfigDefaultAndExplicitValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want int
	}{
		{"unset", "", 1},
		{"disabled", "server:\n  auto_resume_max: 0\n", 0},
		{"custom", "server:\n  auto_resume_max: 3\n", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.yaml")
			write(t, p, tc.yaml+"projects:\n  demo:\n    host_path: /tmp/demo\n")
			cfg, _, err := Load(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Server.EffectiveAutoResumeMax(); got != tc.want {
				t.Fatalf("auto_resume_max = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestTransientPatternsValidation(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		valid   bool
	}{
		{"(?i)at capacity|\\b429\\b", true},
		{"provider busy", true},
		{"(unclosed", false},
	} {
		t.Run(fmt.Sprint(tc.pattern), func(t *testing.T) {
			cfg := &Config{Agents: map[string]AgentConfig{"demo": {TransientErrorPatterns: []string{tc.pattern}}}}
			err := validate(cfg)
			if (err == nil) != tc.valid {
				t.Fatalf("validate(%q) = %v", tc.pattern, err)
			}
			if err != nil && !strings.Contains(err.Error(), "transient_error_patterns") {
				t.Fatalf("wrong validation error: %v", err)
			}
		})
	}
	negative := -1
	err := validate(&Config{Server: ServerConfig{AutoResumeMax: &negative}})
	if err == nil || !strings.Contains(err.Error(), "auto_resume_max") {
		t.Fatalf("negative limit error = %v", err)
	}
}
