package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gookit/gcli/v3"
)

// TestScheduleParamsFromFlags covers the two declaration sources of `schedule add` /
// `schedule edit` (design 2026-10-22 §D6): repeated --param-def values, or one
// --params-file whose YAML carries the full list (choice params need it). The two are
// mutually exclusive, and the whole set is validated before it leaves the CLI.
func TestScheduleParamsFromFlags(t *testing.T) {
	saved := scheduleOpts
	t.Cleanup(func() { scheduleOpts = saved })

	scheduleOpts.paramDefs = gcli.Strings{"CLEAN_DAYS:int=7#保留天数", "CLEAN_APPLY:bool=0"}
	scheduleOpts.paramsFile = ""
	params, err := scheduleParamsFromFlags()
	if err != nil {
		t.Fatalf("--param-def: %v", err)
	}
	if len(params) != 2 {
		t.Fatalf("params = %+v, want 2", params)
	}
	if params[0].Name != "CLEAN_DAYS" || params[0].Type != "int" || params[0].Default != "7" || params[0].Desc != "保留天数" {
		t.Fatalf("first param = %+v", params[0])
	}
	if params[1].Type != "bool" {
		t.Fatalf("second param = %+v", params[1])
	}

	// An invalid declaration is refused before any request is sent.
	scheduleOpts.paramDefs = gcli.Strings{"GOFER_JOB_ID=x"}
	if _, err := scheduleParamsFromFlags(); err == nil {
		t.Fatal("a reserved name must be refused")
	}

	// The YAML file form, choice list included.
	scheduleOpts.paramDefs = nil
	scheduleOpts.paramsFile = filepath.Join(t.TempDir(), "params.yaml")
	body := "- name: CLEAN_ONLY\n  type: choice\n  default: \"\"\n  choices: [\"\", \"a\\\\b\"]\n" +
		"  desc: 只清某个仓库\n- name: CLEAN_DAYS\n  type: int\n  default: \"7\"\n"
	if err := os.WriteFile(scheduleOpts.paramsFile, []byte(body), 0o600); err != nil {
		t.Fatalf("write params file: %v", err)
	}
	params, err = scheduleParamsFromFlags()
	if err != nil {
		t.Fatalf("--params-file: %v", err)
	}
	if len(params) != 2 {
		t.Fatalf("params = %+v, want 2", params)
	}
	if params[0].Type != "choice" || len(params[0].Choices) != 2 || params[0].Choices[1] != `a\b` {
		t.Fatalf("choice param = %+v", params[0])
	}
	if params[1].Default != "7" {
		t.Fatalf("int default = %q", params[1].Default)
	}

	// Both sources at once is a usage error, not a silent merge.
	scheduleOpts.paramDefs = gcli.Strings{"A=1"}
	if _, err := scheduleParamsFromFlags(); err == nil {
		t.Fatal("--param-def with --params-file must be refused")
	}

	// A missing file is reported with the flag that asked for it.
	scheduleOpts.paramDefs = nil
	scheduleOpts.paramsFile = filepath.Join(t.TempDir(), "nope.yaml")
	if _, err := scheduleParamsFromFlags(); err == nil {
		t.Fatal("a missing --params-file must be an error")
	}
}
