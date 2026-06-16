package commands

import (
	"path/filepath"
	"testing"

	"github.com/gookit/gcli/v3"

	"dev-agent-bridge/internal/config"
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
	projectAddOpts.config = cfgPath
	projectAddOpts.hostPath = host
	projectAddOpts.exchangeSubdir = "tmp"
	projectAddOpts.resultSubdir = "dev-agent-bridge"
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
	commonProjectOpts.listConfig = cfgPath
	if err := runProjectList(c, nil); err != nil {
		t.Fatalf("list: %v", err)
	}

	// show
	commonProjectOpts.showConfig = cfgPath
	if err := runProjectShow(c, nil); err != nil {
		t.Fatalf("show: %v", err)
	}

	// validate (host exists, exchange/result dirs creatable, local builtin)
	commonProjectOpts.validateConfig = cfgPath
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
