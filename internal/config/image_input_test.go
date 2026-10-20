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
		"suga": "arg", "suga-acp": "none", "mine": "path", "unknown": "none", "": "none",
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
		ac := AgentConfig{Type: "cli-agent", Command: "x", Args: []string{"{{prompt}}"}, ImageInput: v}
		if v == ImageInputArg {
			ac.ImageArgs = []string{"--image", "{{path}}"}
		}
		return &Config{Agents: map[string]AgentConfig{"a": ac}}
	}
	for _, ok := range []string{"", ImageInputPath, ImageInputNone, ImageInputArg} {
		if err := Validate(mk(ok)); err != nil && err.Error() != "" {
			// Other validation errors are unrelated to image_input; only the field's own matters.
			if strings.Contains(err.Error(), "image_input") || strings.Contains(err.Error(), "image_args") {
				t.Errorf("image_input %q rejected: %v", ok, err)
			}
		}
	}
	err := Validate(mk("sideways"))
	if err == nil || !strings.Contains(err.Error(), "image_input must be path, arg or none") {
		t.Fatalf("image_input sideways: err = %v", err)
	}
}

// An arg agent names its images through image_args: the pair is validated together, and an
// arg agent without a template (or a template without {{path}}) could never open an image.
func TestValidateImageArgs(t *testing.T) {
	mk := func(input string, args []string) *Config {
		return &Config{Agents: map[string]AgentConfig{"a": {
			Type: "cli-agent", Command: "x", Args: []string{"{{prompt}}"}, ImageInput: input, ImageArgs: args,
		}}}
	}
	cases := []struct {
		name  string
		cfg   *Config
		match string
	}{
		{"arg without template", mk(ImageInputArg, nil), "image_input: arg needs image_args"},
		{"template without {{path}}", mk(ImageInputArg, []string{"--image", "./img.png"}), "image_args must contain {{path}}"},
		{"template with {{prompt}}", mk(ImageInputArg, []string{"--image", "{{prompt}}", "{{path}}"}), "image_args must not contain {{prompt}}"},
		{"template without arg mode", mk(ImageInputPath, []string{"--image", "{{path}}"}), "image_args requires image_input: arg"},
	}
	for _, c := range cases {
		err := Validate(c.cfg)
		if err == nil || !strings.Contains(err.Error(), c.match) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.match)
		}
	}
	if err := Validate(mk(ImageInputArg, []string{"--image", "{{path}}"})); err != nil {
		if strings.Contains(err.Error(), "image_args") || strings.Contains(err.Error(), "image_input") {
			t.Errorf("a valid arg pair rejected: %v", err)
		}
	}
}
