package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
)

// TestProjectCmdFlagsBound verifies the project sub-commands register and bind
// their flags (--config etc.) and expose help without panicking.
func TestProjectCmdFlagsBound(t *testing.T) {
	cmd := NewProjectCmd()
	if cmd.Name != "project" {
		t.Fatalf("unexpected name %q", cmd.Name)
	}
	want := map[string]bool{"list": false, "show": false, "add": false, "remove": false, "validate": false}
	for _, sub := range cmd.Subs {
		if _, ok := want[sub.Name]; ok {
			want[sub.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing project sub-command %q", name)
		}
	}
}

// TestProjectAddListShowValidate drives the real runner funcs end-to-end against
// a temp config, exercising add -> list -> show -> validate flag binding.
func TestProjectAddListShowValidate(t *testing.T) {
	host := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "bridge.yaml")

	// Build the project group so Config funcs bind options into the package vars.
	cmd := NewProjectCmd()
	addCmd := findSub(t, cmd, "add")

	// Bind flags via gcli, then set values and invoke the runner directly. The
	// named <key> arg is supplied through gcli's CliArg (argKey reads c.Arg only).
	c := gcli.NewCommand(addCmd.Name, addCmd.Desc, nil)
	if addCmd.Config != nil {
		addCmd.Config(c)
	}
	setArg := func(name, val string) {
		if a := c.Arg(name); a != nil {
			a.WithValue(val)
		}
	}
	setArg("key", "self")
	// The config path is the app-level global -c (config.InputCfgFile); reset it
	// after the test so the package-level global never leaks into other tests.
	config.InputCfgFile = cfgPath
	t.Cleanup(func() { config.InputCfgFile = "" })
	projectAddOpts.hostPath = host
	projectAddOpts.exchangeSubdir = "tmp"
	projectAddOpts.resultSubdir = "gofer"
	projectAddOpts.defaultAgent = ""
	projectAddOpts.allowAgents = gcli.Strings{"exec"}
	projectAddOpts.allowRunners = gcli.Strings{"local"}
	projectAddOpts.allowExec = true
	projectAddOpts.force = false

	if err := runProjectAdd(c, []string{"self"}); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Verify it landed in the config file.
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load after add: %v", err)
	}
	p, ok := cfg.Projects["self"]
	if !ok {
		t.Fatal("project self not saved")
	}
	if p.HostPath != host {
		t.Errorf("host_path = %q want %q", p.HostPath, host)
	}

	// list
	if err := runProjectList(c, nil); err != nil {
		t.Fatalf("list: %v", err)
	}

	// show
	if err := runProjectShow(c, nil); err != nil {
		t.Fatalf("show: %v", err)
	}

	// validate (host exists, exchange/result dirs creatable, local builtin)
	if err := runProjectValidate(c, nil); err != nil {
		t.Fatalf("validate: %v", err)
	}

	// duplicate add without force should fail
	if err := runProjectAdd(c, nil); err == nil {
		t.Fatal("expected duplicate add to fail")
	}

	// missing host-path should fail
	setArg("key", "other")
	projectAddOpts.hostPath = ""
	if err := runProjectAdd(c, nil); err == nil {
		t.Fatal("expected add without host-path to fail")
	}

	// show unknown fails
	setArg("key", "ghost")
	if err := runProjectShow(c, nil); err == nil {
		t.Fatal("expected show of unknown project to fail")
	}
}

// TestProjectAddInteractive drives `project add -i` with piped empty lines (all
// defaults accepted): the project lands in the global config under the cwd dir
// name with host_path = cwd. GOFER_CONFIG_DIR redirects the global config to a
// temp dir so the test never touches the real home.
func TestProjectAddInteractive(t *testing.T) {
	host := t.TempDir()
	cfgDir := t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)

	// chdir into the project dir so cwd-derived defaults (key, host_path) come
	// from it; restore afterwards.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(host); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	// host may be a symlinked temp dir (e.g. /var vs /private/var); use the
	// resolved cwd as the expectation so the assertion matches what the runner sees.
	wantHost, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd after chdir: %v", err)
	}
	wantKey := filepath.Base(wantHost)

	// Feed five blank lines: accept default key, blank container_path,
	// default_agent, allowed_agents (the prompts the runner reads).
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString("\n\n\n\n\n"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = w.Close()
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })

	cmd := NewProjectCmd()
	addCmd := findSub(t, cmd, "add")
	c := gcli.NewCommand(addCmd.Name, addCmd.Desc, nil)
	if addCmd.Config != nil {
		addCmd.Config(c)
	}
	config.InputCfgFile = "" // resolve to the (temp) global config
	projectAddOpts.interactive = true
	projectAddOpts.force = false
	t.Cleanup(func() {
		config.InputCfgFile = ""
		projectAddOpts.interactive = false
	})

	if err := runProjectAdd(c, nil); err != nil {
		t.Fatalf("interactive add: %v", err)
	}

	// It must have been written to the temp global config (UserConfigPath under
	// the temp GOFER_CONFIG_DIR).
	gp, err := config.UserConfigPath()
	if err != nil {
		t.Fatalf("UserConfigPath: %v", err)
	}
	cfg, _, err := config.Load(gp)
	if err != nil {
		t.Fatalf("load global config: %v", err)
	}
	p, ok := cfg.Projects[wantKey]
	if !ok {
		t.Fatalf("interactive add did not register project %q; projects=%v", wantKey, cfg.Projects)
	}
	if p.HostPath != wantHost {
		t.Errorf("host_path = %q want %q", p.HostPath, wantHost)
	}
}

func findSub(t *testing.T, parent *gcli.Command, name string) *gcli.Command {
	t.Helper()
	for _, s := range parent.Subs {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("sub-command %q not found", name)
	return nil
}
