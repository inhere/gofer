package config

import (
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
