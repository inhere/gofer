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
	}
	for want, mut := range bad {
		a := ok
		mut(&a)
		err := validateAgentIntegration("a", a)
		if err == nil || !strings.Contains(err.Error(), strings.Fields(want)[0]) {
			t.Fatalf("%s: err=%v", want, err)
		}
	}
}
