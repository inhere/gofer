package commands

import (
	"reflect"
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

func TestTunnelCmdRegistered(t *testing.T) {
	c := NewTunnelCmd()
	if c.Name != "tunnel" {
		t.Fatal(c.Name)
	}
	if len(c.Subs) != 7 {
		t.Fatalf("subs=%d", len(c.Subs))
	}
	want := map[string]bool{"forward": false, "check": false, "ls": false, "save": false, "saved": false, "forget": false, "stop": false}
	for _, s := range c.Subs {
		want[s.Name] = true
	}
	for n, v := range want {
		if !v {
			t.Fatalf("missing %s", n)
		}
	}
}

// stubPresetClient is a client for a stub hub that holds one saved preset.
func stubPresetClient(t *testing.T, name, worker string, specs []string) *client.Client {
	t.Helper()
	st := newTunnelStub(t)
	st.presets[name] = map[string]any{"name": name, "worker": worker, "specs": specs}
	return client.New(st.URL, "")
}

// TestForwardResolvesSavedProfile drives the resolution a `--name` forward runs
// through: the preset supplies both the worker and every spec, and a name nobody
// saved is an error rather than a silent forward of nothing.
func TestForwardResolvesSavedProfile(t *testing.T) {
	saved := []string{"udp/21845:192.168.0.200:21845", "1502:10.0.0.5:502"}
	cli := stubPresetClient(t, "hw", "w-hw", saved)

	got, err := resolveForward(cli, "hw", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.worker != "w-hw" {
		t.Fatalf("worker=%q want w-hw", got.worker)
	}
	if !reflect.DeepEqual(got.specs, saved) {
		t.Fatalf("specs=%v want %v", got.specs, saved)
	}

	if _, err := resolveForward(cli, "nope", "", nil); err == nil {
		t.Fatal("an unknown preset name must fail, not resolve to nothing")
	}
}

// TestForwardExplicitOverridesProfile pins the precedence rule: what the caller
// typed wins, and a preset only fills what was left out — so a one-off override
// never has to edit the preset first.
func TestForwardExplicitOverridesProfile(t *testing.T) {
	cli := stubPresetClient(t, "hw", "saved-worker", []string{"1502:10.0.0.5:502"})

	got, err := resolveForward(cli, "hw", "explicit-worker", []string{"1600:10.0.0.9:502"})
	if err != nil {
		t.Fatal(err)
	}
	if got.worker != "explicit-worker" || !reflect.DeepEqual(got.specs, []string{"1600:10.0.0.9:502"}) {
		t.Fatalf("explicit values must win, got worker=%q specs=%v", got.worker, got.specs)
	}

	// Partial override: only the worker is given, so the specs still come from the preset.
	got, err = resolveForward(cli, "hw", "explicit-worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.worker != "explicit-worker" || !reflect.DeepEqual(got.specs, []string{"1502:10.0.0.5:502"}) {
		t.Fatalf("preset must fill only what was omitted, got worker=%q specs=%v", got.worker, got.specs)
	}
}

func TestForwardRequiresWorkerAndSpecs(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	if _, err := resolveForward(nil, "", "", nil); err == nil {
		t.Fatal("no worker and no preset must fail")
	}
	if _, err := resolveForward(nil, "", "w-hw", nil); err == nil {
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
	// A socks rule has no fixed target; a same-address rule probes its own address.
	got, err = presetCheckTargets([]string{"socks/1080", "=10.0.0.6:502"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []checkTarget{{network: "tcp", addr: "10.0.0.6:502"}}; !reflect.DeepEqual(got, want) {
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
	cli := stubPresetClient(t, "hw", "w-hw", []string{"1502:10.0.0.5:502"})
	w, err := presetWorker(cli, "hw")
	if err != nil || w != "w-hw" {
		t.Fatalf("worker=%q err=%v", w, err)
	}
	if _, err := presetWorker(cli, "nope"); err == nil {
		t.Fatal("an unknown preset name must fail, not resolve to an empty worker")
	}
}

func TestTunnelCmdRequiresWorker(t *testing.T) {
	if err := runTunnelForward(nil, nil); err == nil {
		t.Fatal("want worker required")
	}
}
