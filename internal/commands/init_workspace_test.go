package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/config"
)

// setTestHome points os.UserHomeDir() at a temp dir on both platforms (HOME on
// unix, USERPROFILE on Windows) so the default workspace is never created in the
// developer's real home.
func setTestHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// TestInitCreatesDefaultWorkspace pins F-g: `gofer init server` creates the default
// workspace directory and registers it as the `default` project, with --workspace /
// GOFER_WORKSPACE overriding the path.
func TestInitCreatesDefaultWorkspace(t *testing.T) {
	det := workerInitFixtureDetector()
	oldDet := initAgentDetector
	t.Cleanup(func() { initAgentDetector = oldDet })
	initAgentDetector = det

	cases := []struct {
		name      string
		workspace func(t *testing.T, home string) (flag, env, want string)
	}{
		{
			name: "default_path",
			workspace: func(t *testing.T, home string) (string, string, string) {
				return "", "", filepath.Join(home, ".gofer", "workspace")
			},
		},
		{
			name: "workspace_flag",
			workspace: func(t *testing.T, home string) (string, string, string) {
				want := filepath.Join(t.TempDir(), "ws-flag")
				return want, "", want
			},
		},
		{
			name: "workspace_env",
			workspace: func(t *testing.T, home string) (string, string, string) {
				want := filepath.Join(t.TempDir(), "ws-env")
				return "", want, want
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			setTestHome(t, home)
			t.Setenv(config.EnvConfigDir, t.TempDir())
			flag, env, want := tc.workspace(t, home)
			if env != "" {
				t.Setenv(config.EnvWorkspace, env)
			} else {
				t.Setenv(config.EnvWorkspace, "")
			}

			outCfg := filepath.Join(t.TempDir(), "config.yaml")
			c := bindCmd(NewInitCmd(buildinfo.Info{}))
			initOpts.config, initOpts.force, initOpts.workspace = outCfg, false, flag
			if err := runInit(c, buildinfo.Info{}); err != nil {
				t.Fatalf("init server: %v", err)
			}

			if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
				t.Fatalf("workspace %s not created (err %v)", want, err)
			}
			cfg, _, err := config.Load(outCfg)
			if err != nil {
				t.Fatalf("load generated config: %v", err)
			}
			def, ok := cfg.Projects["default"]
			if !ok {
				t.Fatalf("generated config has no `default` project: %v", cfg.Projects)
			}
			if def.HostPath != want {
				t.Fatalf("default.host_path = %q, want %q", def.HostPath, want)
			}
			if len(def.AllowedAgents) != 2 || def.AllowedAgents[0] != "claude" || def.AllowedAgents[1] != "codex" {
				t.Fatalf("default.allowed_agents = %v, want the detected [claude codex]", def.AllowedAgents)
			}
		})
	}
}

// TestInitKeepsExistingDefault: an already-present workspace directory is not
// recreated/cleared, and a config that already declares `default` keeps THAT
// host_path instead of gaining a second/overwritten one — no error either way.
func TestInitKeepsExistingDefault(t *testing.T) {
	det := workerInitFixtureDetector()
	oldDet := initAgentDetector
	t.Cleanup(func() { initAgentDetector = oldDet })
	initAgentDetector = det

	t.Run("existing_dir_untouched", func(t *testing.T) {
		home := t.TempDir()
		setTestHome(t, home)
		t.Setenv(config.EnvConfigDir, t.TempDir())
		ws := filepath.Join(home, ".gofer", "workspace")
		if err := os.MkdirAll(ws, 0o755); err != nil {
			t.Fatalf("pre-create workspace: %v", err)
		}
		sentinel := filepath.Join(ws, "keep.txt")
		if err := os.WriteFile(sentinel, []byte("mine"), 0o644); err != nil {
			t.Fatalf("seed sentinel: %v", err)
		}

		outCfg := filepath.Join(t.TempDir(), "config.yaml")
		c := bindCmd(NewInitCmd(buildinfo.Info{}))
		initOpts.config, initOpts.force, initOpts.workspace = outCfg, false, ""
		if err := runInit(c, buildinfo.Info{}); err != nil {
			t.Fatalf("init server over an existing workspace dir must not error: %v", err)
		}
		if b, err := os.ReadFile(sentinel); err != nil || string(b) != "mine" {
			t.Fatalf("sentinel = %q (err %v), want the pre-existing workspace left untouched", b, err)
		}
	})

	t.Run("existing_default_project_preserved", func(t *testing.T) {
		home := t.TempDir()
		setTestHome(t, home)
		t.Setenv(config.EnvConfigDir, t.TempDir())
		custom := t.TempDir()
		outCfg := filepath.Join(t.TempDir(), "config.yaml")
		seed := "projects:\n  default:\n    host_path: " + slashPath(custom) + "\n"
		if err := os.WriteFile(outCfg, []byte(seed), 0o644); err != nil {
			t.Fatalf("seed config: %v", err)
		}

		c := bindCmd(NewInitCmd(buildinfo.Info{}))
		initOpts.config, initOpts.force, initOpts.workspace = outCfg, true, ""
		if err := runInit(c, buildinfo.Info{}); err != nil {
			t.Fatalf("init --force over a config that already declares default: %v", err)
		}
		// The file parses (no duplicate `projects:`/`default:` key) and carries the
		// operator's own default project, not a second one.
		cfg, _, err := config.Load(outCfg)
		if err != nil {
			t.Fatalf("load regenerated config: %v", err)
		}
		// config.Load normalises the separator, so compare in the slash spelling. The
		// regenerated file must still carry the operator's own default project.
		def, ok := cfg.Projects["default"]
		if want := slashPath(custom); !ok || def.HostPath != want {
			t.Fatalf("default project = %+v, want the pre-existing host_path %q", cfg.Projects, want)
		}
	})
}
