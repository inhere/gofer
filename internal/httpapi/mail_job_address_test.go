package httpapi

import "testing"

// A job credential names its recipient directly: a session, a seat, a team or its caller; a
// search address stays refused (it would let the job probe sessions it does not know about).
func TestCheckJobMailAddress(t *testing.T) {
	t.Parallel()
	for _, to := range []string{"session:s1", "caller", "", "seat:review@proj", "team:core"} {
		if err := checkJobMailAddress(to); err != nil {
			t.Errorf("checkJobMailAddress(%q) = %v, want allowed", to, err)
		}
	}
	for _, to := range []string{"topic:login", "path:src", "agent:helper"} {
		if err := checkJobMailAddress(to); err == nil {
			t.Errorf("checkJobMailAddress(%q) allowed, want refused", to)
		}
	}
}
