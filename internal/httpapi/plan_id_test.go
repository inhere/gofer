package httpapi

import (
	"net/http"
	"testing"
)

func TestPlanIDValidation(t *testing.T) {
	s := newTestServer(t, testToken, false)
	post := func(body map[string]string) int {
		t.Helper()
		resp := do(t, s, http.MethodPost, "/v1/plans", testToken, body)
		resp.Body.Close()
		return resp.StatusCode
	}

	if got := post(map[string]string{"plan_id": "short-id"}); got != http.StatusBadRequest { // 8 chars
		t.Fatalf("8-char plan id status=%d, want 400", got)
	}
	if got := post(map[string]string{"plan_id": "bad/id-123"}); got != http.StatusBadRequest {
		t.Fatalf("plan id with '/' status=%d, want 400", got)
	}
	if got := post(map[string]string{"plan_id": "valid-id9"}); got != http.StatusOK {
		t.Fatalf("9-char plan id status=%d, want 200", got)
	}
	if got := post(map[string]string{"plan_id": "valid-id9"}); got != http.StatusConflict {
		t.Fatalf("duplicate plan id status=%d, want 409", got)
	}
	if got := post(map[string]string{"title": "auto id"}); got != http.StatusOK {
		t.Fatalf("server-generated plan id status=%d, want 200", got)
	}
}
