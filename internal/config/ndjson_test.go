package config

import (
	"strings"
	"testing"
)

// TestValidateRejectsBadNDJSONKnobs: the two stream-layout switches of the ndjson
// capture (bd h-aii-525u) are validated at load, like output_format — a typo must
// fail the serve start instead of silently keeping the default layout.
func TestValidateRejectsBadNDJSONKnobs(t *testing.T) {
	bad := []struct {
		name string
		ac   AgentConfig
		want string
	}{
		{"unknown ndjson_events_to", AgentConfig{OutputFormat: OutputFormatNDJSON, NDJSONEventsTo: "stder"}, "ndjson_events_to"},
		{"unknown ndjson_stdout", AgentConfig{OutputFormat: OutputFormatNDJSON, NDJSONStdout: "final"}, "ndjson_stdout"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			err := validate(&Config{Agents: map[string]AgentConfig{"omp": tc.ac}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate() = %v, want an error naming %q", err, tc.want)
			}
		})
	}

	ok := []AgentConfig{
		{OutputFormat: OutputFormatNDJSON},
		{OutputFormat: OutputFormatNDJSON, NDJSONEventsTo: NDJSONEventsStderr, NDJSONStdout: NDJSONStdoutFinalText},
		{OutputFormat: OutputFormatNDJSON, NDJSONEventsTo: NDJSONEventsStdout, NDJSONStdout: NDJSONStdoutEvents},
		{OutputFormat: OutputFormatText, NDJSONStdoutPath: "result.result"},
	}
	for i, ac := range ok {
		if err := validate(&Config{Agents: map[string]AgentConfig{"omp": ac}}); err != nil {
			t.Fatalf("case %d: validate() = %v, want nil", i, err)
		}
	}
}
