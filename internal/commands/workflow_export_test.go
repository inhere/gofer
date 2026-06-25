package commands

import (
	"encoding/json"
	"strings"
	"testing"

	yaml "github.com/goccy/go-yaml"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
)

func exportSpec() workflow.Spec {
	return workflow.Spec{
		Title: "exp",
		Steps: []workflow.StepSpec{
			{Name: "gen", ProjectKey: "smoke", Agent: "exec", Runner: "local",
				Cmd: []string{"sh", "-c", "echo gen"}, FanOut: 2, Join: "all"},
			{Name: "test", ProjectKey: "smoke", Agent: "exec", Runner: "local",
				Cmd:       []string{"sh", "-c", "echo ${steps.1.result_dir}"},
				OnFailure: "retry", Retry: &job.RetryPolicy{MaxAttempts: 2, BackoffSec: []int{1}}},
		},
	}
}

// TestMarshalWorkflowSpecYAMLRoundTrips asserts the default (yaml) export is the SAME
// shape `wf run` consumes: it uses snake_case keys, carries the v2 fields, and
// re-parses (yaml.Unmarshal) back into an equal spec — the round-trip `wf export`→
// `wf run` relies on (the whole point of defaulting to yaml).
func TestMarshalWorkflowSpecYAMLRoundTrips(t *testing.T) {
	spec := exportSpec()
	for _, format := range []string{"", "yaml", "yml", "YAML"} {
		out, err := marshalWorkflowSpec(spec, format)
		if err != nil {
			t.Fatalf("format %q: %v", format, err)
		}
		s := string(out)
		if strings.HasPrefix(strings.TrimSpace(s), "{") {
			t.Fatalf("format %q produced JSON, want YAML: %s", format, s)
		}
		// snake_case keys + v2 fields present (matches `wf run` yaml tags).
		for _, want := range []string{"project_key: smoke", "fan_out: 2", "join: all", "on_failure: retry", "max_attempts: 2"} {
			if !strings.Contains(s, want) {
				t.Fatalf("format %q yaml missing %q:\n%s", format, want, s)
			}
		}
		// exactly one trailing newline normalisation (helper trims; no trailing \n).
		if strings.HasSuffix(s, "\n") {
			t.Fatalf("format %q: expected no trailing newline, got %q", format, s)
		}
		// Round-trip: yaml re-parses into the same spec `wf run` would submit.
		var back workflow.Spec
		if err := yaml.Unmarshal(out, &back); err != nil {
			t.Fatalf("format %q: re-parse yaml: %v", format, err)
		}
		if back.Title != spec.Title || len(back.Steps) != 2 ||
			back.Steps[0].FanOut != 2 || back.Steps[0].Join != "all" ||
			back.Steps[1].OnFailure != "retry" || back.Steps[1].Retry == nil ||
			back.Steps[1].Retry.MaxAttempts != 2 {
			t.Fatalf("format %q: round-trip lost v2 fields: %+v", format, back)
		}
	}
}

// TestMarshalWorkflowSpecJSON asserts --format json emits valid indented JSON.
func TestMarshalWorkflowSpecJSON(t *testing.T) {
	out, err := marshalWorkflowSpec(exportSpec(), "json")
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(out)), "{") {
		t.Fatalf("json format did not produce JSON: %s", out)
	}
	var back workflow.Spec
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("json re-parse: %v", err)
	}
	if back.Title != "exp" || len(back.Steps) != 2 {
		t.Fatalf("json round-trip wrong: %+v", back)
	}
}

// TestMarshalWorkflowSpecUnknownFormat asserts an unknown format is rejected (not
// silently defaulted) so a typo surfaces instead of producing the wrong shape.
func TestMarshalWorkflowSpecUnknownFormat(t *testing.T) {
	if _, err := marshalWorkflowSpec(exportSpec(), "xml"); err == nil {
		t.Fatal("unknown format xml should error")
	} else if !strings.Contains(err.Error(), "want yaml or json") {
		t.Fatalf("error should name valid formats, got: %v", err)
	}
}
