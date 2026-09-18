package agent

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestNdjsonStdoutDefaultsOmpAssistantText (bd h-aii-lvo9): omp's ndjson stdout
// defaults to assistant_text (every assistant message), claude keeps final_text
// (result.result is authoritative), and an explicit value is never overridden.
func TestNdjsonStdoutDefaultsOmpAssistantText(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		agent config.AgentConfig
		want  string
	}{
		{"omp defaults to assistant_text", "omp",
			config.AgentConfig{Type: TypeCLIAgent, Command: "omp", OutputFormat: config.OutputFormatNDJSON}, config.NDJSONStdoutAssistantText},
		{"omp by command base name", "my-omp",
			config.AgentConfig{Type: TypeCLIAgent, Command: "/opt/bin/omp", OutputFormat: config.OutputFormatNDJSON}, config.NDJSONStdoutAssistantText},
		{"explicit final_text wins", "omp",
			config.AgentConfig{Type: TypeCLIAgent, Command: "omp", OutputFormat: config.OutputFormatNDJSON, NDJSONStdout: config.NDJSONStdoutFinalText}, config.NDJSONStdoutFinalText},
		{"claude stays unset (final_text)", "claude",
			config.AgentConfig{Type: TypeCLIAgent, Command: "claude", OutputFormat: config.OutputFormatNDJSON}, ""},
		{"text output untouched", "omp",
			config.AgentConfig{Type: TypeCLIAgent, Command: "omp"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Agents: map[string]config.AgentConfig{tc.key: tc.agent}}
			got, ok := ResolveAgent(cfg, tc.key)
			if !ok {
				t.Fatalf("ResolveAgent(%q) not found", tc.key)
			}
			if got.NDJSONStdout != tc.want {
				t.Fatalf("ndjson_stdout = %q, want %q", got.NDJSONStdout, tc.want)
			}
		})
	}
}
