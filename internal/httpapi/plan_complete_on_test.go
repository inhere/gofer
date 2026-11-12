package httpapi

import (
	"net/http"
	"testing"
)

// TestPlanCompleteOnCreateAndPatch: complete_on defaults to job, can be set at create and
// by PATCH, always shows on the wire, and an invalid value is a 400 on both routes.
func TestPlanCompleteOnCreateAndPatch(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	var p struct {
		CompleteOn string `json:"complete_on"`
	}

	resp := do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]string{"plan_id": "plan-co-default"})
	decode(t, resp, &p)
	if p.CompleteOn != "job" {
		t.Fatalf("default complete_on = %q, want job", p.CompleteOn)
	}

	resp = do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]string{"plan_id": "plan-co-merge", "complete_on": "merge"})
	decode(t, resp, &p)
	if p.CompleteOn != "merge" {
		t.Fatalf("created complete_on = %q, want merge", p.CompleteOn)
	}

	resp = do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]string{"plan_id": "plan-co-bad-new", "complete_on": "later"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create with invalid complete_on status=%d, want 400", resp.StatusCode)
	}

	resp = do(t, s, http.MethodPatch, "/v1/plans/plan-co-default", testToken, map[string]string{"complete_on": "merge"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch complete_on status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &p)
	if p.CompleteOn != "merge" {
		t.Fatalf("patched complete_on = %q, want merge", p.CompleteOn)
	}

	resp = do(t, s, http.MethodPatch, "/v1/plans/plan-co-default", testToken, map[string]string{"complete_on": "bogus"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("patch invalid complete_on status=%d, want 400", resp.StatusCode)
	}
	// The refused PATCH changed nothing.
	resp = do(t, s, http.MethodGet, "/v1/plans/plan-co-default", testToken, nil)
	decode(t, resp, &p)
	if p.CompleteOn != "merge" {
		t.Fatalf("complete_on after a refused patch = %q, want merge", p.CompleteOn)
	}
}
