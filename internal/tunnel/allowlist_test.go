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
