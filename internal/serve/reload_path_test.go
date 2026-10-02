package serve

import "testing"

func TestReloadPathConsistent(t *testing.T) {
	const loaded = `C:\\gofer\\config.yaml`
	if got := reloadPath(Opts{CfgPath: loaded, ReloadPath: `C:\\other.yaml`}); got != loaded {
		t.Fatalf("reloadPath=%q, want loaded path %q", got, loaded)
	}
	if got := reloadPath(Opts{CfgPath: loaded}); got != loaded {
		t.Fatalf("reloadPath without legacy field=%q, want %q", got, loaded)
	}
}
