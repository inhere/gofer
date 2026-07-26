package httpapi

import (
	"net/http"
	"testing"
)

// TestDecisionStatusMatrix covers the decision-channel HTTP contract (T1):
// validation 400s, dangling plan_id 404, timeout clamp, get/answer 404 vs
// already-answered 409 (M5: deliberately split, unlike interactions).
func TestDecisionStatusMatrix(t *testing.T) {
	s := newTestServer(t, testToken, false)

	resp := do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]string{"plan_id": "plan-dec"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create plan status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// Missing title / question -> 400.
	resp = do(t, s, http.MethodPost, "/v1/decisions", testToken, map[string]any{"question": "q"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("ask without title status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/decisions", testToken, map[string]any{"title": "t"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("ask without question status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Dangling plan_id -> 404 (L2).
	resp = do(t, s, http.MethodPost, "/v1/decisions", testToken, map[string]any{
		"plan_id": "plan-nope", "title": "t", "question": "q",
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("ask with dangling plan status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// Valid ask against the plan -> 200 OPEN, options echoed, dec- id.
	resp = do(t, s, http.MethodPost, "/v1/decisions", testToken, map[string]any{
		"plan_id": "plan-dec", "title": "pick", "question": "which?",
		"options": []string{"a", "b"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ask status=%d, want 200", resp.StatusCode)
	}
	var asked decisionView
	decode(t, resp, &asked)
	if asked.ID == "" || asked.ID[:4] != "dec-" {
		t.Fatalf("asked id = %q, want dec-*", asked.ID)
	}
	if asked.State != "OPEN" || asked.PlanID != "plan-dec" {
		t.Fatalf("asked state/plan mismatch: %+v", asked)
	}
	if len(asked.Options) != 2 || asked.Options[0] != "a" {
		t.Fatalf("options not echoed: %+v", asked.Options)
	}
	if asked.TimeoutSec != 1800 {
		t.Fatalf("default timeout = %d, want 1800", asked.TimeoutSec)
	}

	// Out-of-range timeout is clamped (HTTP face covered by the store clamp).
	resp = do(t, s, http.MethodPost, "/v1/decisions", testToken, map[string]any{
		"title": "t", "question": "q", "timeout_sec": 99999999,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ask big timeout status=%d, want 200", resp.StatusCode)
	}
	var clamped decisionView
	decode(t, resp, &clamped)
	if clamped.TimeoutSec != 86400 {
		t.Fatalf("clamped timeout = %d, want 86400", clamped.TimeoutSec)
	}
	resp = do(t, s, http.MethodPost, "/v1/decisions", testToken, map[string]any{
		"title": "t", "question": "q", "timeout_sec": 0,
	})
	var defaulted decisionView
	decode(t, resp, &defaulted)
	if defaulted.TimeoutSec != 1800 {
		t.Fatalf("timeout_sec=0 -> %d, want default 1800", defaulted.TimeoutSec)
	}

	// Get unknown -> 404; get existing -> 200 with options deserialised.
	resp = do(t, s, http.MethodGet, "/v1/decisions/dec-nope", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get unknown status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/decisions/"+asked.ID, testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status=%d, want 200", resp.StatusCode)
	}
	var got decisionView
	decode(t, resp, &got)
	if got.ID != asked.ID || got.State != "OPEN" || len(got.Options) != 2 {
		t.Fatalf("get mismatch: %+v", got)
	}

	// List filters: invalid state -> 400; state/plan filters narrow results.
	resp = do(t, s, http.MethodGet, "/v1/decisions?state=WEIRD", testToken, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("list invalid state status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/decisions?state=OPEN&plan_id=plan-dec", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d, want 200", resp.StatusCode)
	}
	var listed struct {
		Decisions []decisionView `json:"decisions"`
	}
	decode(t, resp, &listed)
	if len(listed.Decisions) != 1 || listed.Decisions[0].ID != asked.ID {
		t.Fatalf("list plan-dec OPEN mismatch: %+v", listed.Decisions)
	}
	resp = do(t, s, http.MethodGet, "/v1/decisions?plan_id=plan-other", testToken, nil)
	decode(t, resp, &listed)
	if len(listed.Decisions) != 0 {
		t.Fatalf("list plan-other should be empty: %+v", listed.Decisions)
	}

	// Answer unknown -> 404.
	resp = do(t, s, http.MethodPost, "/v1/decisions/dec-nope/answer", testToken, map[string]string{"answer": "x"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("answer unknown status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// Empty answer -> 400.
	resp = do(t, s, http.MethodPost, "/v1/decisions/"+asked.ID+"/answer", testToken, map[string]string{"answer": " "})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty answer status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Answer -> 200 ANSWERED with answered_by from the caller context.
	resp = do(t, s, http.MethodPost, "/v1/decisions/"+asked.ID+"/answer", testToken, map[string]string{"answer": "a"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("answer status=%d, want 200", resp.StatusCode)
	}
	var answered decisionView
	decode(t, resp, &answered)
	if answered.State != "ANSWERED" || answered.Answer != "a" ||
		answered.AnsweredBy != "default" || answered.AnsweredAt <= 0 {
		t.Fatalf("answered mismatch: %+v", answered)
	}

	// Re-answer -> 409 (M5: distinct from unknown-id 404).
	resp = do(t, s, http.MethodPost, "/v1/decisions/"+asked.ID+"/answer", testToken, map[string]string{"answer": "b"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("re-answer status=%d, want 409", resp.StatusCode)
	}
	resp.Body.Close()

	// ANSWERED drops out of the OPEN list.
	resp = do(t, s, http.MethodGet, "/v1/decisions?state=OPEN", testToken, nil)
	decode(t, resp, &listed)
	for _, d := range listed.Decisions {
		if d.ID == asked.ID {
			t.Fatalf("answered decision still in OPEN list: %+v", d)
		}
	}

	// Plan detail inlines the decisions array (additive).
	resp = do(t, s, http.MethodGet, "/v1/plans/plan-dec", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get plan status=%d, want 200", resp.StatusCode)
	}
	var detail struct {
		Decisions []decisionView `json:"decisions"`
	}
	decode(t, resp, &detail)
	if len(detail.Decisions) != 1 || detail.Decisions[0].ID != asked.ID ||
		detail.Decisions[0].State != "ANSWERED" {
		t.Fatalf("plan detail decisions mismatch: %+v", detail.Decisions)
	}
}
