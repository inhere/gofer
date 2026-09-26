package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestJobRunFallsBackToDefaultProject pins F-g's project resolution: with no -p and a
// cwd that matches no configured project, `job run` falls back to the `default`
// project (the init-time workspace) and says so on stderr — while a config WITHOUT a
// `default` project keeps the pre-existing "--project/-p is required" error.
func TestJobRunFallsBackToDefaultProject(t *testing.T) {
	ws := t.TempDir()
	runCmd := NewApp("test").GetCommand("job").GetCommand("run")
	jobRunOpts = jobRunFlags{}
	t.Cleanup(func() { jobRunOpts = jobRunFlags{} })

	oldStderr := jobRunStderr
	t.Cleanup(func() { jobRunStderr = oldStderr })

	t.Run("falls_back", func(t *testing.T) {
		cfgPath := writeRawConfig(t, "projects:\n  default:\n    host_path: "+slashPath(ws)+"\n    allowed_runners: [local]\n")
		config.InputCfgFile = cfgPath
		t.Cleanup(func() { config.InputCfgFile = "" })
		var buf bytes.Buffer
		jobRunStderr = &buf
		jobRunOpts.agent = "exec"

		autoDetectJobProject(runCmd)
		if jobRunOpts.project != "default" {
			t.Fatalf("project = %q, want the `default` fallback", jobRunOpts.project)
		}
		if !strings.Contains(buf.String(), "default") {
			t.Fatalf("stderr hint = %q, want it to name the default project", buf.String())
		}
		if err := validateJobRunRequired(); err != nil {
			t.Fatalf("the fallback must satisfy the -p requirement, got %v", err)
		}
	})

	t.Run("no_default_project", func(t *testing.T) {
		cfgPath := writeRawConfig(t, "projects:\n  other:\n    host_path: "+slashPath(ws)+"\n    allowed_runners: [local]\n")
		config.InputCfgFile = cfgPath
		t.Cleanup(func() { config.InputCfgFile = "" })
		jobRunOpts.project = ""
		var buf bytes.Buffer
		jobRunStderr = &buf

		autoDetectJobProject(runCmd)
		if jobRunOpts.project != "" {
			t.Fatalf("project = %q, want it left empty (no default project exists)", jobRunOpts.project)
		}
		if buf.Len() != 0 {
			t.Fatalf("stderr = %q, want no hint when nothing was resolved", buf.String())
		}
		err := validateJobRunRequired()
		if err == nil {
			t.Fatal("a config without `default` must keep the --project/-p required error")
		}
		if !strings.Contains(err.Error(), "-p") {
			t.Fatalf("error = %v, want the pre-existing --project/-p message", err)
		}
	})
}
