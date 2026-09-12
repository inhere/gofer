package tunnel

import "testing"

func TestValidateTarget(t *testing.T) {
	for _, tt := range []struct {
		s  string
		ok bool
	}{{"[fe80::1]:502", true}, {"h:0", false}, {"h:65536", false}, {"h:http", false}, {"h:502x", false}, {"fe80::1:502", false}, {":502", false}} {
		t.Run(tt.s, func(t *testing.T) {
			if (ValidateTarget(tt.s) == nil) != tt.ok {
				t.Fatalf("unexpected result")
			}
		})
	}
}
func TestParseAllowlist(t *testing.T) {
	for _, s := range []string{"10.0.0.1:502", "10.0.0.0/24:*", "[fe80::/64]:502", "plc.local:502", "127.0.0.1:1217"} {
		if _, e := ParseAllowlist([]string{s}); e != nil {
			t.Fatal(e)
		}
	}
	for _, s := range []string{"10.0.0.1", "10.0.0.1:0", "10.0.0.1:abc", ":502"} {
		if _, e := ParseAllowlist([]string{s}); e == nil {
			t.Fatalf("expected %s error", s)
		}
	}
}
func TestAllows(t *testing.T) {
	a, _ := ParseAllowlist([]string{"10.0.0.1:502", "PLC.LOCAL:503", "10.0.0.0/24:504", "*:505", "[fe80::1]:506"})
	cases := []struct {
		s    string
		want bool
	}{{"10.0.0.1:502", true}, {"plc.local:503", true}, {"PLC.LOCAL:503", true}, {"10.0.0.2:504", true}, {"10.1.0.2:504", false}, {"plc.local:504", false}, {"h:505", true}, {"h:506", false}, {"[fe80::1]:506", true}, {"10.0.0.1:507", false}}
	for _, c := range cases {
		if a.Allows(c.s) != c.want {
			t.Errorf("%s", c.s)
		}
	}
	if a.Empty() || !(&Allowlist{}).Empty() || !(*Allowlist)(nil).Empty() || (*Allowlist)(nil).Allows("h:1") {
		t.Fatal("empty mismatch")
	}
}

// TestParseAllowlistPortListsAndRanges pins the compact port syntax: one entry may
// carry a comma separated list and inclusive ranges, so a device answering on
// several ports stays one line. "*" inside a list is rejected on purpose — an
// entry that mixes them no longer shows its real breadth at a glance.
func TestParseAllowlistPortListsAndRanges(t *testing.T) {
	for _, s := range []string{
		"192.168.0.100:502,1217,11740",
		"192.168.0.100:11740-11743",
		"192.168.0.100:502, 11740-11743",
		"10.0.0.0/24:502,503",
		"[fe80::1]:502,503",
	} {
		if _, e := ParseAllowlist([]string{s}); e != nil {
			t.Errorf("%s should parse: %v", s, e)
		}
	}
	for _, s := range []string{
		"192.168.0.100:502,",
		"192.168.0.100:,502",
		"192.168.0.100:502,abc",
		"192.168.0.100:11743-11740",
		"192.168.0.100:0-5",
		"192.168.0.100:1-",
		"192.168.0.100:*,502",
	} {
		if _, e := ParseAllowlist([]string{s}); e == nil {
			t.Errorf("%s should be rejected", s)
		}
	}
}

// TestAllowsPortListsAndRanges checks membership, including both range edges and
// the ports just outside them: a range must not leak one port either side.
func TestAllowsPortListsAndRanges(t *testing.T) {
	a, e := ParseAllowlist([]string{"192.168.0.100:502,1217,11740", "10.0.0.0/24:11740-11743", "plc.local:502,503"})
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range []struct {
		s    string
		want bool
	}{
		{"192.168.0.100:502", true}, {"192.168.0.100:1217", true}, {"192.168.0.100:11740", true},
		{"192.168.0.100:503", false}, {"192.168.0.100:11741", false},
		{"10.0.0.5:11740", true}, {"10.0.0.5:11743", true},
		{"10.0.0.5:11739", false}, {"10.0.0.5:11744", false},
		{"plc.local:503", true}, {"PLC.LOCAL:502", true}, {"plc.local:504", false},
	} {
		if a.Allows(c.s) != c.want {
			t.Errorf("%s: want %v", c.s, c.want)
		}
	}
}
