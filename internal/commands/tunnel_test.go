package commands

import "testing"

func TestTunnelCmdRegistered(t *testing.T) {
	c := NewTunnelCmd()
	if c.Name != "tunnel" {
		t.Fatal(c.Name)
	}
	if len(c.Subs) != 3 {
		t.Fatalf("subs=%d", len(c.Subs))
	}
	want := map[string]bool{"forward": false, "check": false, "ls": false}
	for _, s := range c.Subs {
		want[s.Name] = true
	}
	for n, v := range want {
		if !v {
			t.Fatalf("missing %s", n)
		}
	}
}
func TestTunnelCmdRequiresWorker(t *testing.T) {
	if err := runTunnelForward(nil, nil); err == nil {
		t.Fatal("want worker required")
	}
}
