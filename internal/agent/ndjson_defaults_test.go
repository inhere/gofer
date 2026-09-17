package agent

import (
	"reflect"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
)

// TestNdjsonBuiltinDefaultsOmpAndClaude pins the built-in ndjson keep lists and
// projectors (bd h-aii-rpky / bd h-aii-525u) and, just as importantly, that the
// keep list is INERT unless the agent asks for `output_format: ndjson` — a text
// agent must never have output filtered. The projector is resolved by the same
// key/command fallback, for every agent (an unknown one gets the generic
// projector, which projects nothing on its own).
func TestNdjsonBuiltinDefaultsOmpAndClaude(t *testing.T) {
	ompWant := []string{"session", "tool_execution_start", "tool_execution_end", "message_end", "turn_end", "agent_end", "advisor_cost_changed"}
	claudeWant := []string{"system", "assistant", "user", "result"}

	cases := []struct {
		name      string
		key       string
		agent     config.AgentConfig
		want      []string
		projector string
	}{
		{
			name:      "omp by agent key",
			key:       "omp",
			agent:     config.AgentConfig{Type: TypeCLIAgent, Command: "omp", OutputFormat: config.OutputFormatNDJSON},
			want:      ompWant,
			projector: ndjsonfilter.ProjectorOMP,
		},
		{
			name:      "omp by command base name",
			key:       "my-omp",
			agent:     config.AgentConfig{Type: TypeCLIAgent, Command: `C:\tools\omp.exe`, OutputFormat: config.OutputFormatNDJSON},
			want:      ompWant,
			projector: ndjsonfilter.ProjectorOMP,
		},
		{
			name:      "claude by agent key",
			key:       "claude",
			agent:     config.AgentConfig{Type: TypeCLIAgent, Command: "claude", OutputFormat: config.OutputFormatNDJSON},
			want:      claudeWant,
			projector: ndjsonfilter.ProjectorClaude,
		},
		{
			name:      "claude by command base name",
			key:       "tty-claude",
			agent:     config.AgentConfig{Type: TypeCLIAgent, Command: "/usr/local/bin/claude", OutputFormat: config.OutputFormatNDJSON},
			want:      claudeWant,
			projector: ndjsonfilter.ProjectorClaude,
		},
		{
			name:      "explicit ndjson_keep wins over the built-in list",
			key:       "omp",
			agent:     config.AgentConfig{Type: TypeCLIAgent, Command: "omp", OutputFormat: config.OutputFormatNDJSON, NDJSONKeep: []string{"result"}},
			want:      []string{"result"},
			projector: ndjsonfilter.ProjectorOMP,
		},
		{
			name:      "format text keeps nothing filtered",
			key:       "omp",
			agent:     config.AgentConfig{Type: TypeCLIAgent, Command: "omp", OutputFormat: config.OutputFormatText},
			projector: ndjsonfilter.ProjectorOMP, // resolved by name; only consulted for ndjson
		},
		{
			name:      "unset format is text",
			key:       "claude",
			agent:     config.AgentConfig{Type: TypeCLIAgent, Command: "claude"},
			projector: ndjsonfilter.ProjectorClaude,
		},
		{
			name:      "unknown agent has no built-in list",
			key:       "opencode",
			agent:     config.AgentConfig{Type: TypeCLIAgent, Command: "opencode", OutputFormat: config.OutputFormatNDJSON},
			projector: ndjsonfilter.ProjectorGeneric,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Agents: map[string]config.AgentConfig{tc.key: tc.agent}}
			got, ok := ResolveAgent(cfg, tc.key)
			if !ok {
				t.Fatalf("ResolveAgent(%q) not found", tc.key)
			}
			if !reflect.DeepEqual(got.NDJSONKeep, tc.want) {
				t.Fatalf("ndjson_keep = %v, want %v", got.NDJSONKeep, tc.want)
			}
			if got.NDJSONOutput() != (tc.agent.OutputFormat == config.OutputFormatNDJSON) {
				t.Fatalf("NDJSONOutput() = %v for output_format %q", got.NDJSONOutput(), tc.agent.OutputFormat)
			}
			if p := NDJSONProjectorFor(tc.key, tc.agent); p != tc.projector {
				t.Fatalf("NDJSONProjectorFor(%q) = %q, want %q", tc.key, p, tc.projector)
			}
		})
	}

	// The resolved config is a copy: the built-in list must not be written back
	// into the loaded config (a later save would freeze it into gofer.yaml).
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"omp": {Type: TypeCLIAgent, Command: "omp", OutputFormat: config.OutputFormatNDJSON},
	}}
	_, _ = ResolveAgent(cfg, "omp")
	if cfg.Agents["omp"].NDJSONKeep != nil {
		t.Fatalf("ResolveAgent mutated the loaded config: ndjson_keep = %v", cfg.Agents["omp"].NDJSONKeep)
	}
}
