package httpapi

import "testing"

// X2: the steward credential gets exactly the two new reads and the one new write; every
// other route that could talk to a session or touch issues stays default-denied.
func TestStewardAllowlistX2IsExactAndDefaultDeny(t *testing.T) {
	for _, ok := range []string{"POST /v1/session-ask", "GET /v1/issues", "GET /v1/issues/*"} {
		if !stewardRouteAllowed(ok) {
			t.Fatalf("%s must be allowed for the steward", ok)
		}
	}
	for _, denied := range []string{
		"POST /v1/sessions/*/messages", "POST /v1/sessions/*/say", "POST /v1/sessions/*/deliver", "POST /v1/jobs/*/say",
		"POST /v1/issues", "PUT /v1/issues/*", "DELETE /v1/issues/*", "PUT /v1/tracker/issues/*", "POST /v1/tracker/sync",
		"POST /v1/jobs", "PUT /v1/config/work", "GET /v1/tracker/issues",
	} {
		if stewardRouteAllowed(denied) {
			t.Fatalf("%s must stay denied for the steward", denied)
		}
	}
}
