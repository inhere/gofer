package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/color"
	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	configtmpl "github.com/inhere/gofer/config"
	"github.com/inhere/gofer/internal/config"
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
	haveValidate, haveShow, haveEdit, haveInfo := false, false, false, false
	for _, s := range cmd.Subs {
		switch s.Name {
		case "validate":
			haveValidate = true
		case "show":
			haveShow = true
		case "edit":
			haveEdit = true
		case "info":
			haveInfo = true
		}
	}
	if !haveValidate {
		t.Fatal("missing config validate sub-command")
	}
	if !haveShow {
		t.Fatal("missing config show sub-command")
	}
	if !haveEdit {
		t.Fatal("missing config edit sub-command")
	}
	if !haveInfo {
		t.Fatal("missing config info sub-command")
	}
}

// TestConfigSubsRegisteredViaApp verifies the config subcommands are reachable
// from the assembled app via GetCommand (the plan's registration check form):
// app → config → edit/info non-nil.
func TestConfigSubsRegisteredViaApp(t *testing.T) {
	app := NewApp("test")
	cfg := app.GetCommand("config")
	if cfg == nil {
		t.Fatal("app missing config command")
	}
	if cfg.GetCommand("edit") == nil {
		t.Fatal("config edit not registered on the app")
	}
	if cfg.GetCommand("info") == nil {
		t.Fatal("config info not registered on the app")
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

// TestInitTemplateMapping verifies the target→template/path resolution: server
// (and the bare default) → ExampleYAML/.gofer.yaml; worker → WorkerExampleYAML/
// worker.yaml; anything else → not ok.
func TestInitTemplateMapping(t *testing.T) {
	for _, target := range []string{"", "server"} {
		tmpl, path, ok := initTemplate(target)
		if !ok || tmpl != configtmpl.ExampleYAML || path != DefaultInitConfigPath {
			t.Errorf("initTemplate(%q) = (_, %q, %v), want server template/.gofer.yaml", target, path, ok)
		}
	}
	tmpl, path, ok := initTemplate("worker")
	if !ok || tmpl != configtmpl.WorkerExampleYAML || path != DefaultWorkerConfigPath {
		t.Errorf("initTemplate(worker) = (_, %q, %v), want worker template/worker.yaml", path, ok)
	}
	if _, _, ok := initTemplate("nope"); ok {
		t.Error("initTemplate(nope) should not be ok")
	}
}

// TestInitWorkerTarget verifies `gofer init worker` writes the embedded worker
// template (not the server one).
func TestInitWorkerTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yaml")

	c := bindCmd(NewInitCmd())
	c.Arg("target").WithValue("worker")
	initOpts.config = path
	initOpts.force = false
	t.Cleanup(func() { initOpts.config = ""; initOpts.force = false })

	if err := runInit(c, nil); err != nil {
		t.Fatalf("init worker: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written worker config: %v", err)
	}
	if string(got) != configtmpl.WorkerExampleYAML {
		t.Fatal("written content != embedded worker template")
	}
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

// TestInitServerGlobalPath verifies `init server --global -f` writes to the
// user-global config path (config.UserConfigPath()), creating the dir. GOFER_CONFIG_DIR
// redirects the global dir to a temp dir so the test never touches the real home.
func TestInitServerGlobalPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)

	want, err := config.UserConfigPath()
	if err != nil {
		t.Fatalf("UserConfigPath: %v", err)
	}

	c := bindCmd(NewInitCmd())
	initOpts.config = ""
	initOpts.global = true
	initOpts.force = true
	t.Cleanup(func() { initOpts.config = ""; initOpts.global = false; initOpts.force = false })

	if err := runInit(c, nil); err != nil {
		t.Fatalf("init server --global: %v", err)
	}
	// The global path == UserConfigPath() and the file was actually written there.
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("global config not written at UserConfigPath() %s: %v", want, err)
	}
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read global config: %v", err)
	}
	if string(got) != configtmpl.ExampleYAML {
		t.Fatal("global config content != embedded server template")
	}
}

// TestInitWorkerGlobalNoEffect verifies --global has no effect for the worker
// target: it still writes ./worker.yaml (worker has no global discovery).
func TestInitWorkerGlobalNoEffect(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	// Point the global dir elsewhere; --global must NOT redirect the worker output there.
	t.Setenv(config.EnvConfigDir, t.TempDir())

	c := bindCmd(NewInitCmd())
	c.Arg("target").WithValue("worker")
	initOpts.config = ""
	initOpts.global = true
	initOpts.force = true
	t.Cleanup(func() { initOpts.config = ""; initOpts.global = false; initOpts.force = false })

	if err := runInit(c, nil); err != nil {
		t.Fatalf("init worker --global: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, DefaultWorkerConfigPath)); err != nil {
		t.Fatalf("worker --global should still write ./worker.yaml: %v", err)
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
	config.InputCfgFile = cfgPath
	t.Cleanup(func() { config.InputCfgFile = "" })
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
	config.InputCfgFile = cfgPath
	t.Cleanup(func() { config.InputCfgFile = "" })
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
	config.InputCfgFile = cfgPath
	t.Cleanup(func() { config.InputCfgFile = "" })
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
	config.InputCfgFile = cfgPath
	t.Cleanup(func() { config.InputCfgFile = "" })
	if err := runConfigValidate(c, nil); err != nil {
		t.Fatalf("empty config should validate, got: %v", err)
	}
}

// TestConfigValidateWorkerGood: a valid worker.yaml (worker_id + urls + token +
// a project with an existing host_path and allowed_runners:[local]) validates
// clean via `config validate worker`.
func TestConfigValidateWorkerGood(t *testing.T) {
	host := t.TempDir()
	wcYAML := "" +
		"worker_id: w1\n" +
		"server_link:\n" +
		"  urls: [ws://127.0.0.1:LIVE-PORT/v1/workers/connect]\n" +
		"  token: tok\n" +
		"projects:\n" +
		"  w1:\n" +
		"    host_path: " + host + "\n" +
		"    allowed_runners: [local]\n" +
		"    allow_exec: true\n"
	cfgPath := writeRawConfig(t, wcYAML)

	c := bindCmd(NewConfigCmd().Subs[0])
	c.Arg("target").WithValue("worker")
	config.InputCfgFile = cfgPath
	t.Cleanup(func() { config.InputCfgFile = "" })
	if err := runConfigValidate(c, nil); err != nil {
		t.Fatalf("expected good worker config to validate, got: %v", err)
	}
}

// TestConfigValidateWorkerBadToken: a worker.yaml whose token_env points at an
// unset env var (and no inline token) fails — the #1 connect cause.
func TestConfigValidateWorkerBadToken(t *testing.T) {
	host := t.TempDir()
	wcYAML := "" +
		"worker_id: w1\n" +
		"server_link:\n" +
		"  urls: [ws://127.0.0.1:LIVE-PORT/v1/workers/connect]\n" +
		"  token_env: GOFER_TEST_UNSET_WORKER_TOKEN\n" +
		"projects:\n" +
		"  w1:\n" +
		"    host_path: " + host + "\n" +
		"    allowed_runners: [local]\n"
	cfgPath := writeRawConfig(t, wcYAML)

	c := bindCmd(NewConfigCmd().Subs[0])
	c.Arg("target").WithValue("worker")
	config.InputCfgFile = cfgPath
	t.Cleanup(func() { config.InputCfgFile = "" })
	err := runConfigValidate(c, nil)
	if err == nil {
		t.Fatal("expected validation failure for unresolvable worker token")
	}
	assertCodedExit(t, err)
}

// TestConfigValidateUnknownTarget: an unrecognised target is a coded error.
func TestConfigValidateUnknownTarget(t *testing.T) {
	c := bindCmd(NewConfigCmd().Subs[0])
	c.Arg("target").WithValue("nope")
	err := runConfigValidate(c, nil)
	if err == nil {
		t.Fatal("expected unknown validate target to error")
	}
	assertCodedExit(t, err)
}

// captureOutput redirects gcli/color output (c.Printf writes via
// color.SimplePrinter → color.Output) to a buffer for the duration of fn and
// returns what was printed.
func captureOutput(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	color.SetOutput(&buf)
	defer color.ResetOutput()
	fn()
	return buf.String()
}

// TestConfigShowMergesOverlay: `config show --project <key>` applies the
// per-project overlay (diagnostic, not a ruling) and prints the EFFECTIVE config:
// result_subdir comes from the overlay (out), allowed_agents comes from the
// global config (overlay cannot grant 准入).
func TestConfigShowMergesOverlay(t *testing.T) {
	host := t.TempDir()
	// Project overlay in the project dir overrides result_subdir only.
	overlay := "result_subdir: out\n"
	if err := os.WriteFile(filepath.Join(host, config.ProjectOverlayName), []byte(overlay), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	cfgYAML := "" +
		"projects:\n" +
		"  siv:\n" +
		"    host_path: " + host + "\n" +
		"    result_subdir: global_out\n" +
		"    allowed_agents: [claude]\n"
	cfgPath := writeRawConfig(t, cfgYAML)

	c := bindCmd(NewConfigCmd().Subs[1]) // Subs[1] == show
	c.Arg("key").WithValue("siv")
	config.InputCfgFile = cfgPath
	t.Cleanup(func() { config.InputCfgFile = "" })

	out := captureOutput(t, func() {
		if err := runConfigShow(c, nil); err != nil {
			t.Fatalf("config show: %v", err)
		}
	})

	if !strings.Contains(out, "result_subdir: out") {
		t.Fatalf("expected overlay-merged result_subdir=out, got:\n%s", out)
	}
	if !strings.Contains(out, "allowed_agents: [claude]") {
		t.Fatalf("expected global allowed_agents=[claude], got:\n%s", out)
	}
}

// TestConfigShowUnknownProject: an unregistered key is a coded (non-zero) error.
func TestConfigShowUnknownProject(t *testing.T) {
	cfgPath := writeRawConfig(t, "server:\n  addr: 0.0.0.0:8765\n")
	c := bindCmd(NewConfigCmd().Subs[1])
	c.Arg("key").WithValue("ghost")
	config.InputCfgFile = cfgPath
	t.Cleanup(func() { config.InputCfgFile = "" })

	err := runConfigShow(c, nil)
	if err == nil {
		t.Fatal("expected config show to error on unknown project")
	}
	assertCodedExit(t, err)
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
