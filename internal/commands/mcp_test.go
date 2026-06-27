package commands

import "testing"

// TestMcpUseClient checks the E28 D1/D2 mode-decision truth table: client mode
// iff not --standalone AND a server addr (flag/env) is resolved.
func TestMcpUseClient(t *testing.T) {
	cases := []struct {
		name       string
		standalone bool
		serverAddr string
		want       bool
	}{
		{"standalone overrides addr (D2 escape)", true, "x", false},
		{"no addr, no standalone -> standalone", false, "", false},
		{"addr set, no standalone -> client (D1)", false, "x", true},
		{"standalone, no addr -> standalone", true, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpUseClient(tc.standalone, tc.serverAddr); got != tc.want {
				t.Fatalf("mcpUseClient(%v, %q) = %v, want %v",
					tc.standalone, tc.serverAddr, got, tc.want)
			}
		})
	}
}

// TestMcpCmdFlags verifies `mcp` binds the connection + escape-hatch flags
// (--server/--token via bindServerFlags, --standalone via mcpOpts) so client
// mode is reachable without re-binding.
func TestMcpCmdFlags(t *testing.T) {
	c := NewMcpCmd()
	c.Config(c) // apply the Config closure to register flags
	for _, name := range []string{"server", "token", "standalone", "config"} {
		if c.Flags.LookupFlag(name) == nil {
			t.Errorf("mcp command missing --%s flag", name)
		}
	}
}
