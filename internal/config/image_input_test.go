package config

import (
	"strings"
	"testing"
)

func TestEffectiveImageInput(t *testing.T) {
	cfg := &Config{Agents: map[string]AgentConfig{
		"mine":   {ImageInput: "path"},
		"claude": {ImageInput: "none"}, // an explicit value beats the built-in table
	}}
	cases := map[string]string{
		"claude": "none", "claude-acp": "path", "codex": "path", "codex-acp": "path",
		"suga": "none", "suga-acp": "none", "mine": "path", "unknown": "none", "": "none",
	}
	for key, want := range cases {
		if got := cfg.EffectiveImageInput(key); got != want {
			t.Errorf("EffectiveImageInput(%q) = %q, want %q", key, got, want)
		}
	}
	var nilCfg *Config
	if nilCfg.EffectiveImageInput("claude") != "path" {
		t.Error("a nil config still answers from the built-in table")
	}
}

func TestValidateImageInputValue(t *testing.T) {
	mk := func(v string) *Config {
		return &Config{Agents: map[string]AgentConfig{"a": {Type: "cli-agent", Command: "x", Args: []string{"{{prompt}}"}, ImageInput: v}}}
	}
	for _, ok := range []string{"", "path", "none"} {
		if err := Validate(mk(ok)); err != nil && err.Error() != "" {
			// Other validation errors are unrelated to image_input; only the field's own matters.
			if strings.Contains(err.Error(), "image_input") {
				t.Errorf("image_input %q rejected: %v", ok, err)
			}
		}
	}
	err := Validate(mk("sideways"))
	if err == nil || !strings.Contains(err.Error(), "image_input must be path or none") {
		t.Fatalf("image_input sideways: err = %v", err)
	}
}
