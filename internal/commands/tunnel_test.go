package commands

import (
	"reflect"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestTunnelCmdRegistered(t *testing.T) {
	c := NewTunnelCmd()
	if c.Name != "tunnel" {
		t.Fatal(c.Name)
	}
	if len(c.Subs) != 6 {
		t.Fatalf("subs=%d", len(c.Subs))
	}
	want := map[string]bool{"forward": false, "check": false, "ls": false, "save": false, "saved": false, "forget": false}
	for _, s := range c.Subs {
		want[s.Name] = true
	}
	for n, v := range want {
		if !v {
			t.Fatalf("missing %s", n)
		}
	}
}

// TestForwardResolvesSavedProfile drives the resolution a `--name` forward runs
// through: the preset supplies both the worker and every spec, and a name nobody
// saved is an error rather than a silent forward of nothing.
func TestForwardResolvesSavedProfile(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	saved := []string{"udp/21845:192.168.0.200:21845", "1502:10.0.0.5:502"}
	if e := config.UpsertTunnel("hw", config.TunnelProfile{Worker: "w-hw", Specs: saved}, false); e != nil {
		t.Fatal(e)
	}

	worker, specs, err := resolveForward("hw", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if worker != "w-hw" {
		t.Fatalf("worker=%q want w-hw", worker)
	}
	if !reflect.DeepEqual(specs, saved) {
		t.Fatalf("specs=%v want %v", specs, saved)
	}

	if _, _, err := resolveForward("nope", "", nil); err == nil {
		t.Fatal("an unknown preset name must fail, not resolve to nothing")
	}
}

// TestForwardExplicitOverridesProfile pins the precedence rule: what the caller
// typed wins, and a preset only fills what was left out — so a one-off override
// never has to edit the preset first.
func TestForwardExplicitOverridesProfile(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	if e := config.UpsertTunnel("hw", config.TunnelProfile{Worker: "saved-worker", Specs: []string{"1502:10.0.0.5:502"}}, false); e != nil {
		t.Fatal(e)
	}

	worker, specs, err := resolveForward("hw", "explicit-worker", []string{"1600:10.0.0.9:502"})
	if err != nil {
		t.Fatal(err)
	}
	if worker != "explicit-worker" || !reflect.DeepEqual(specs, []string{"1600:10.0.0.9:502"}) {
		t.Fatalf("explicit values must win, got worker=%q specs=%v", worker, specs)
	}

	// Partial override: only the worker is given, so the specs still come from the preset.
	worker, specs, err = resolveForward("hw", "explicit-worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if worker != "explicit-worker" || !reflect.DeepEqual(specs, []string{"1502:10.0.0.5:502"}) {
		t.Fatalf("preset must fill only what was omitted, got worker=%q specs=%v", worker, specs)
	}
}

func TestForwardRequiresWorkerAndSpecs(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	if _, _, err := resolveForward("", "", nil); err == nil {
		t.Fatal("no worker and no preset must fail")
	}
	if _, _, err := resolveForward("", "w-hw", nil); err == nil {
		t.Fatal("a worker without specs must fail")
	}
}

// TestPresetCheckTargetsUseSpecTargets is the regression guard for `check --name`:
// a preset holds forward SPECS, so the address to probe is the spec's target half.
// Probing the spec text itself would dial a local port number as if it were a host.
func TestPresetCheckTargetsUseSpecTargets(t *testing.T) {
	got, err := presetCheckTargets([]string{"udp/21845:192.168.0.200:21845", "1502:10.0.0.5:502", "0.0.0.0:1600:[fe80::1]:502"})
	if err != nil {
		t.Fatal(err)
	}
	want := []checkTarget{
		{network: "udp", addr: "192.168.0.200:21845"},
		{network: "tcp", addr: "10.0.0.5:502"},
		{network: "tcp", addr: "[fe80::1]:502"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if _, err := presetCheckTargets([]string{"not-a-spec"}); err == nil {
		t.Fatal("a malformed spec must surface, not be probed")
	}
}

func TestParseCheckTarget(t *testing.T) {
	if got := parseCheckTarget("udp/192.168.0.200:21845"); got != (checkTarget{network: "udp", addr: "192.168.0.200:21845"}) {
		t.Fatalf("%+v", got)
	}
	if got := parseCheckTarget("10.0.0.5:502"); got != (checkTarget{network: "tcp", addr: "10.0.0.5:502"}) {
		t.Fatalf("%+v", got)
	}
}

// TestPresetWorker covers the lookup `check <target> -n <preset>` uses: the target
// is typed on the command line, so only the worker comes from the preset.
func TestPresetWorker(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	if e := config.UpsertTunnel("hw", config.TunnelProfile{Worker: "w-hw", Specs: []string{"1502:10.0.0.5:502"}}, false); e != nil {
		t.Fatal(e)
	}
	w, err := presetWorker("hw")
	if err != nil || w != "w-hw" {
		t.Fatalf("worker=%q err=%v", w, err)
	}
	if _, err := presetWorker("nope"); err == nil {
		t.Fatal("an unknown preset name must fail, not resolve to an empty worker")
	}
}

func TestTunnelCmdRequiresWorker(t *testing.T) {
	if err := runTunnelForward(nil, nil); err == nil {
		t.Fatal("want worker required")
	}
}
