package config

import (
	"slices"
	"strings"
	"testing"
)

func TestValidateAgentIntegration(t *testing.T) {
	ok := AgentConfig{Type: "cli-agent", Command: "myagent", OutputFormat: OutputFormatNDJSON,
		NDJSONUsagePath: "result.usage", TranscriptDialect: "generic", InjectProcess: []string{"myagent"}, SessionFamily: "my-fam.1"}
	if err := validateAgentIntegration("a", ok); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	bad := map[string]func(*AgentConfig){
		"transcript_dialect":  func(a *AgentConfig) { a.TranscriptDialect = "weird" },
		"ndjson_usage_path":   func(a *AgentConfig) { a.NDJSONUsagePath = "a..b" },
		"needs output_format": func(a *AgentConfig) { a.OutputFormat = "" },
		"only applies":        func(a *AgentConfig) { a.Type = "acp-agent" },
		"inject_process":      func(a *AgentConfig) { a.InjectProcess = []string{"/usr/bin/x"} },
		"inject_process ext":  func(a *AgentConfig) { a.InjectProcess = []string{"x.exe"} },
		"session_family":      func(a *AgentConfig) { a.SessionFamily = "a b" },
		"deliver_offline_match": func(a *AgentConfig) {
			a.DeliverCommand = []string{"send", "{{session_id}}", "{{text}}"}
			a.DeliverOfflineMatch = "(unclosed"
		},
	}
	a := ok
	a.DeliverOfflineMatch = "gone"
	if err := validateAgentIntegration("a", a); err == nil || !strings.Contains(err.Error(), "needs deliver_command") {
		t.Fatalf("offline match without deliver_command: %v", err)
	}
	a.DeliverCommand = []string{"send", "{{session_id}}", "{{text}}"}
	if err := validateAgentIntegration("a", a); err != nil {
		t.Fatalf("valid offline match rejected: %v", err)
	}
	for want, mut := range bad {
		a := ok
		mut(&a)
		err := validateAgentIntegration("a", a)
		if err == nil || !strings.Contains(err.Error(), strings.Fields(want)[0]) {
			t.Errorf("%s: err=%v", want, err)
		}
	}
	a = ok
	a.DeliverCommand = []string{"send", "{{session_id}}", "{{text}}"}
	a.DeliverImageArgs = []string{"--image", "/tmp/fixed.png"}
	if err := validateAgentIntegration("a", a); err == nil || !strings.Contains(err.Error(), "deliver_image_args") {
		t.Fatalf("deliver_image_args without {{image}} accepted: %v", err)
	}
	a.DeliverImageArgs = []string{"--image", "{{image}}"}
	if err := validateAgentIntegration("a", a); err != nil {
		t.Fatalf("valid deliver_image_args rejected: %v", err)
	}
}

// An agent that spells out exactly the built-in delivery keeps the built-in image half;
// a different command is not guessed at.
func TestEffectiveDeliverImageArgsInheritsBuiltin(t *testing.T) {
	same := []string{"send", "--session", "{{session_id}}", "--source", "gofer"}
	cfg := &Config{Agents: map[string]AgentConfig{
		"suga":         {Type: "cli-agent", Command: "suga", DeliverCommand: same, DeliverStdin: true},
		"suga-diff":    {Type: "cli-agent", Command: "suga", DeliverCommand: []string{"send", "--session", "{{session_id}}"}, DeliverStdin: true},
		"suga-own":     {Type: "cli-agent", Command: "suga", DeliverCommand: same, DeliverStdin: true, DeliverImageArgs: []string{"--pic", "{{image}}"}},
		"suga-wrap":    {Type: "cli-agent", Command: "/opt/bin/suga", DeliverCommand: same, DeliverStdin: true},
		"suga-nostdin": {Type: "cli-agent", Command: "suga", DeliverCommand: same},
	}}
	want := []string{"--image", "{{image}}"}
	if got := cfg.EffectiveDeliverImageArgs("suga"); !slices.Equal(got, want) {
		t.Errorf("same command: got %v, want %v", got, want)
	}
	if got := cfg.EffectiveDeliverImageArgs("suga-wrap"); !slices.Equal(got, want) {
		t.Errorf("same command via command base: got %v, want %v", got, want)
	}
	if got := cfg.EffectiveDeliverImageArgs("suga-diff"); len(got) != 0 {
		t.Errorf("different command must not inherit: got %v", got)
	}
	if got := cfg.EffectiveDeliverImageArgs("suga-nostdin"); len(got) != 0 {
		t.Errorf("different stdin mode must not inherit: got %v", got)
	}
	if got := cfg.EffectiveDeliverImageArgs("suga-own"); !slices.Equal(got, []string{"--pic", "{{image}}"}) {
		t.Errorf("own image args must win: got %v", got)
	}
}

func TestBuiltinSugaDeliverOfflineMatch(t *testing.T) {
	cmd, _, stdin, match := BuiltinDeliver("suga")
	if cmd == nil || !stdin || match != "session process is not online" {
		t.Fatalf("builtin suga delivery = %v stdin=%v match=%q", cmd, stdin, match)
	}
}

// deliver_offline_match alone is valid on an `extends: builtin` agent whose template has
// a delivery command; it stays an error otherwise.
func TestValidateOfflineMatchWithExtendsBuiltin(t *testing.T) {
	base := AgentConfig{Type: "cli-agent", Command: "suga", DeliverOfflineMatch: "gone"}
	for _, ext := range []string{ExtendsBuiltin, "builtin:suga"} {
		a := base
		a.Extends = ext
		if err := validateAgentIntegration("suga", a); err != nil {
			t.Errorf("extends %q: %v", ext, err)
		}
	}
	a := base
	a.Extends = ExtendsBuiltin
	if err := validateAgentIntegration("nodeliver", a); err == nil || !strings.Contains(err.Error(), "needs deliver_command") {
		t.Errorf("template without delivery: %v", err)
	}
	if err := validateAgentIntegration("suga", base); err == nil || !strings.Contains(err.Error(), "needs deliver_command") {
		t.Errorf("no extends: %v", err)
	}
}
