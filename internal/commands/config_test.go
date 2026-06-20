package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	configtmpl "github.com/inhere/gofer/config"
)

// bindCmd builds a gcli.Command and runs its Config func so the package-level
// option vars are bound (mirrors the project_test.go pattern).
func bindCmd(def *gcli.Command) *gcli.Command {
	c := gcli.NewCommand(def.Name, def.Desc, nil)
	if def.Config != nil {
		def.Config(c)
	}
	return c
}

// TestConfigCmdRegistered verifies the config group exposes the validate sub.
func TestConfigCmdRegistered(t *testing.T) {
	cmd := NewConfigCmd()
	if cmd.Name != "config" {
		t.Fatalf("unexpected name %q", cmd.Name)
	}
	found := false
	for _, s := range cmd.Subs {
		if s.Name == "validate" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing config validate sub-command")
	}
}

// TestInitWritesEmbeddedTemplate verifies init writes the embedded template
// verbatim, refuses to overwrite without --force, and overwrites with --force.
func TestInitWritesEmbeddedTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gofer.yaml")

	c := bindCmd(NewInitCmd())

	// First write: file does not exist → succeeds, content == embed.
	initOpts.config = path
	initOpts.force = false
	if err := runInit(c, nil); err != nil {
		t.Fatalf("init: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written config: %v", err)
	}
	if string(got) != configtmpl.ExampleYAML {
		t.Fatalf("written content != embedded template")
	}

	// Second write without --force → coded error (non-zero exit), file unchanged.
	if err := runInit(c, nil); err == nil {
		t.Fatal("expected init to refuse overwriting an existing config")
	} else {
		assertCodedExit(t, err)
	}

	// With --force → overwrite succeeds (sentinel proves it was rewritten).
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	initOpts.force = true
	if err := runInit(c, nil); err != nil {
		t.Fatalf("init --force: %v", err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != configtmpl.ExampleYAML {
		t.Fatal("--force did not rewrite the template")
	}
	initOpts.force = false
}

// TestInitDefaultPath verifies init defaults to ./.gofer.yaml when no --config.
func TestInitDefaultPath(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	c := bindCmd(NewInitCmd())
	initOpts.config = ""
	initOpts.force = false
	if err := runInit(c, nil); err != nil {
		t.Fatalf("init default: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, DefaultInitConfigPath)); err != nil {
		t.Fatalf("default config not written: %v", err)
	}
}

// writeRawConfig writes a raw config YAML body to a temp file and returns its
// path (the reload_test writeConfig helper takes a *config.Config instead).
func writeRawConfig(t *testing.T, yamlBody string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bridge.yaml")
	if err := os.WriteFile(path, []byte(yamlBody), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestConfigValidateGoodConfig: a config whose project paths exist validates
// clean (no error == zero exit).
func TestConfigValidateGoodConfig(t *testing.T) {
	host := t.TempDir()
	cfgYAML := "" +
		"projects:\n" +
		"  ok:\n" +
		"    host_path: " + host + "\n" +
		"    allowed_runners: [local]\n"
	cfgPath := writeRawConfig(t, cfgYAML)

	c := bindCmd(NewConfigCmd().Subs[0])
	configValidateOpts.config = cfgPath
	if err := runConfigValidate(c, nil); err != nil {
		t.Fatalf("expected good config to validate, got: %v", err)
	}
}

// TestConfigValidateMissingHostPath: a project whose host_path does not exist
// fails validation with a coded (non-zero exit) error.
func TestConfigValidateMissingHostPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	cfgYAML := "" +
		"projects:\n" +
		"  bad:\n" +
		"    host_path: " + missing + "\n"
	cfgPath := writeRawConfig(t, cfgYAML)

	c := bindCmd(NewConfigCmd().Subs[0])
	configValidateOpts.config = cfgPath
	err := runConfigValidate(c, nil)
	if err == nil {
		t.Fatal("expected validation failure for missing host_path")
	}
	assertCodedExit(t, err)
}

// TestConfigValidateUnknownAgent: a project referencing an undeclared agent
// fails validation with a coded error.
func TestConfigValidateUnknownAgent(t *testing.T) {
	host := t.TempDir()
	cfgYAML := "" +
		"projects:\n" +
		"  bad:\n" +
		"    host_path: " + host + "\n" +
		"    default_agent: ghost\n"
	cfgPath := writeRawConfig(t, cfgYAML)

	c := bindCmd(NewConfigCmd().Subs[0])
	configValidateOpts.config = cfgPath
	err := runConfigValidate(c, nil)
	if err == nil {
		t.Fatal("expected validation failure for unknown agent")
	}
	assertCodedExit(t, err)
}

// TestConfigValidateNoProjects: an empty config validates clean (no projects to
// check, structural Load validate passes).
func TestConfigValidateNoProjects(t *testing.T) {
	cfgPath := writeRawConfig(t, "server:\n  addr: 0.0.0.0:8765\n")
	c := bindCmd(NewConfigCmd().Subs[0])
	configValidateOpts.config = cfgPath
	if err := runConfigValidate(c, nil); err != nil {
		t.Fatalf("empty config should validate, got: %v", err)
	}
}

// assertCodedExit checks the error carries a non-zero exit code so the process
// exits non-zero (gcli reads errorx.ErrorCoder).
func assertCodedExit(t *testing.T, err error) {
	t.Helper()
	coder, ok := err.(errorx.ErrorCoder)
	if !ok {
		t.Fatalf("error %T is not coded (no non-zero exit)", err)
	}
	if coder.Code() == 0 {
		t.Fatalf("coded error has zero exit code")
	}
}
