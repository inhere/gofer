package config

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestStageConfigDefaults(t *testing.T) {
	cfg := &Config{Projects: map[string]ProjectConfig{"plain": {}, "off": {Stage: &ProjectStage{Enabled: boolPtrT(false)}}}}
	if !cfg.StageEnabledFor("plain") || !cfg.StageEnabledFor("unknown") {
		t.Error("stage must default to enabled")
	}
	if cfg.StageEnabledFor("off") {
		t.Error("explicit enabled:false must turn the stage prompts off")
	}
	if got := cfg.StageVerifyTemplatesFor("plain"); len(got) != 1 || got[0] != "verify" {
		t.Errorf("default verify templates = %v, want [verify]", got)
	}
	// The returned list is a copy: mutating it must not change the default.
	cfg.StageVerifyTemplatesFor("plain")[0] = "changed"
	if got := cfg.StageVerifyTemplatesFor("plain"); got[0] != "verify" {
		t.Errorf("default verify templates were mutated: %v", got)
	}
}

func TestStageConfigYAMLAndValidation(t *testing.T) {
	var cfg Config
	src := "projects:\n  a:\n    host_path: /x\n    stage:\n      enabled: false\n      verify_templates: [gofer-verify, extra]\n"
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.StageEnabledFor("a") {
		t.Error("enabled:false was not read")
	}
	if got := cfg.StageVerifyTemplatesFor("a"); len(got) != 2 || got[0] != "gofer-verify" {
		t.Errorf("verify_templates = %v", got)
	}
	if _, ok := FieldPolicyFor("projects.*.stage"); !ok {
		t.Error("no field policy for projects.*.stage")
	}
	for name, tpl := range map[string][]string{"slash": {"a/b"}, "blank": {" "}, "dup": {"v", "v"}, "dotdot": {".."}} {
		err := validate(&Config{Projects: map[string]ProjectConfig{"p": {HostPath: "/tmp/p", Stage: &ProjectStage{VerifyTemplates: tpl}}}})
		if err == nil || !strings.Contains(err.Error(), "stage.verify_templates") {
			t.Errorf("%s: err = %v, want it to mention stage.verify_templates", name, err)
		}
	}
	if err := validate(&Config{Projects: map[string]ProjectConfig{"p": {HostPath: "/tmp/p", Stage: &ProjectStage{VerifyTemplates: []string{"gofer-verify"}}}}}); err != nil {
		t.Errorf("valid stage block rejected: %v", err)
	}
}

func boolPtrT(b bool) *bool { return &b }
