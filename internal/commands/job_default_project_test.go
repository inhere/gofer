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

	// Not declared in the config: the server serves a built-in `default` (the default
	// workspace), so the CLI falls back to it too instead of demanding -p.
	t.Run("builtin_default_when_undeclared", func(t *testing.T) {
		cfgPath := writeRawConfig(t, "projects:\n  other:\n    host_path: "+slashPath(ws)+"\n    allowed_runners: [local]\n")
		config.InputCfgFile = cfgPath
		t.Cleanup(func() { config.InputCfgFile = "" })
		builtinWS := t.TempDir()
		t.Setenv(config.EnvWorkspace, builtinWS)
		jobRunOpts.project = ""
		var buf bytes.Buffer
		jobRunStderr = &buf

		autoDetectJobProject(runCmd)
		if jobRunOpts.project != "default" {
			t.Fatalf("project = %q, want the built-in `default` fallback", jobRunOpts.project)
		}
		if !strings.Contains(buf.String(), builtinWS) {
			t.Fatalf("stderr hint = %q, want it to name the default workspace", buf.String())
		}
		if err := validateJobRunRequired(); err != nil {
			t.Fatalf("the fallback must satisfy the -p requirement, got %v", err)
		}
	})
}
