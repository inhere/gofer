package project

import "testing"

// "server" / "local" are reserved for the built-in runner, so an allowlist entry
// spelled either way (any case) always means the built-in.
func TestAllowsLocalRunner(t *testing.T) {
	t.Parallel()
	cases := []struct {
		allowed []string
		want    bool
	}{
		{nil, true},
		{[]string{"server"}, true},
		{[]string{"local"}, true},
		{[]string{" Server "}, true},
		{[]string{"w1"}, false},
		{[]string{"w1", "server"}, true},
	}
	for i, tc := range cases {
		if got := AllowsLocalRunner(tc.allowed); got != tc.want {
			t.Errorf("case %d: AllowsLocalRunner(%v) = %v, want %v", i, tc.allowed, got, tc.want)
		}
	}
}
