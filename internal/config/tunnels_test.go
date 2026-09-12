package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUserTunnelsPathHonorsConfigDirEnv(t *testing.T) {
	d := t.TempDir()
	t.Setenv(EnvConfigDir, d)
	p, e := UserTunnelsPath()
	if e != nil || p != filepath.Join(d, "tunnels.yaml") {
		t.Fatalf("%s %v", p, e)
	}
}

func TestTunnelProfilesRoundTrip(t *testing.T) {
	d := t.TempDir()
	t.Setenv(EnvConfigDir, d)
	in := &Tunnels{Forwards: map[string]TunnelProfile{
		"hw": {Worker: "w-hw", Specs: []string{"udp/21845:192.168.0.200:21845", "1502:10.0.0.5:502"}, Note: "现场 HMI + PLC"},
	}}
	if e := SaveTunnels(in); e != nil {
		t.Fatal(e)
	}
	out, e := LoadTunnels()
	if e != nil || !reflect.DeepEqual(in, out) {
		t.Fatalf("%#v %v", out, e)
	}
	// A machine that never saved a preset must load an empty set, not fail.
	t.Setenv(EnvConfigDir, t.TempDir())
	empty, e := LoadTunnels()
	if e != nil || len(empty.Forwards) != 0 {
		t.Fatalf("missing file should be an empty set: %#v %v", empty, e)
	}
}

// TestSaveTunnelProfileRejectsBadSpec checks the validation that keeps an
// unrunnable preset out of the file: a spec that ParseForwardSpec rejects must
// fail the save, and nothing may be written.
func TestSaveTunnelProfileRejectsBadSpec(t *testing.T) {
	d := t.TempDir()
	t.Setenv(EnvConfigDir, d)
	if e := UpsertTunnel("hw", TunnelProfile{Worker: "w-hw", Specs: []string{"1502:10.0.0.5:502", "not-a-spec"}}, false); e == nil {
		t.Fatal("a malformed forward spec must be rejected")
	}
	if _, e := os.Stat(filepath.Join(d, "tunnels.yaml")); e == nil {
		t.Fatal("a rejected preset must not create the file")
	}
	// The other ways a preset can be unrunnable.
	for name, p := range map[string]TunnelProfile{
		"bad name": {Worker: "w-hw", Specs: []string{"1502:10.0.0.5:502"}},
		"nospecs":  {Worker: "w-hw"},
		"noworker": {Specs: []string{"1502:10.0.0.5:502"}},
	} {
		if e := UpsertTunnel(name, p, false); e == nil {
			t.Errorf("%q should be rejected", name)
		}
	}
}

func TestTunnelProfileUpsertAndDelete(t *testing.T) {
	d := t.TempDir()
	t.Setenv(EnvConfigDir, d)
	p := TunnelProfile{Worker: "w-hw", Specs: []string{"1502:10.0.0.5:502"}}
	if e := UpsertTunnel("hw", p, false); e != nil {
		t.Fatal(e)
	}
	// Same name twice is refused unless forced, so a typo cannot silently replace
	// a working preset.
	if e := UpsertTunnel("hw", TunnelProfile{Worker: "other", Specs: []string{"1600:10.0.0.9:502"}}, false); e == nil {
		t.Fatal("an existing preset must not be overwritten without --force")
	}
	if e := UpsertTunnel("hw", TunnelProfile{Worker: "other", Specs: []string{"1600:10.0.0.9:502"}}, true); e != nil {
		t.Fatal(e)
	}
	got, _ := LoadTunnels()
	if got.Forwards["hw"].Worker != "other" {
		t.Fatalf("--force did not overwrite: %#v", got.Forwards["hw"])
	}

	if e := DeleteTunnel("hw"); e != nil {
		t.Fatal(e)
	}
	if e := DeleteTunnel("hw"); e == nil {
		t.Fatal("deleting a preset that is not there must report it")
	}
	x, _ := LoadTunnels()
	if len(x.Forwards) != 0 {
		t.Fatal(x)
	}
}
