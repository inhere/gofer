package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// TestSubmitJobResult: a job reports how it ended with its OWN credential; another job's
// credential and every user-class caller are refused; a second report is a 409; the report
// is then visible on GET /v1/jobs/{id}.
func TestSubmitJobResult(t *testing.T) {
	s := newCeilingServer(t)
	grantAll(s)
	tokA := seedCredentialJob(t, s, "res-job-a", "p1", "plain")
	tokB := seedCredentialJob(t, s, "res-job-b", "p1", "plain")
	body := map[string]any{"decision": "human", "reason": "needs a call", "action_required": "pick one", "findings": []string{"f1"}}

	// Another job's credential: 403, and nothing is recorded.
	if status, resp := ceilReq(t, s, tokB, http.MethodPost, "/v1/jobs/res-job-a/result", body); status != http.StatusForbidden {
		t.Fatalf("other job's credential = %d (%s), want 403", status, resp)
	}
	// A user-class caller cannot speak for a job.
	if status, resp := ceilReq(t, s, ceilTokPlain, http.MethodPost, "/v1/jobs/res-job-a/result", body); status != http.StatusForbidden {
		t.Fatalf("user token = %d (%s), want 403", status, resp)
	}
	// Invalid decision / missing reason: 400.
	if status, resp := ceilReq(t, s, tokA, http.MethodPost, "/v1/jobs/res-job-a/result", map[string]any{"decision": "maybe", "reason": "x"}); status != http.StatusBadRequest {
		t.Fatalf("bad decision = %d (%s), want 400", status, resp)
	}
	if status, resp := ceilReq(t, s, tokA, http.MethodPost, "/v1/jobs/res-job-a/result", map[string]any{"decision": "pass"}); status != http.StatusBadRequest {
		t.Fatalf("missing reason = %d (%s), want 400", status, resp)
	}
	// Own credential: accepted.
	resp := do(t, s, http.MethodPost, "/v1/jobs/res-job-a/result", tokA, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own credential = %d (%s), want 200", resp.StatusCode, readBody(t, resp))
	}
	var got job.JobResult
	decode(t, resp, &got)
	if got.ResultDecision != "human" || got.ResultReason != "needs a call" || got.ResultAttempt != 1 {
		t.Fatalf("response result fields = %q %q %d", got.ResultDecision, got.ResultReason, got.ResultAttempt)
	}
	// Second report for the same job: 409, the first one stays.
	if status, resp := ceilReq(t, s, tokA, http.MethodPost, "/v1/jobs/res-job-a/result", map[string]any{"decision": "pass", "reason": "again"}); status != http.StatusConflict {
		t.Fatalf("second report = %d (%s), want 409", status, resp)
	}
	resp = do(t, s, http.MethodGet, "/v1/jobs/res-job-a", ceilTokPlain, nil)
	var shown job.JobResult
	decode(t, resp, &shown)
	if shown.ResultDecision != "human" || shown.ResultReason != "needs a call" || !strings.Contains(shown.ResultReportJSON, "pick one") {
		t.Fatalf("GET /v1/jobs/{id} result fields = %+v", shown)
	}
	// Job B was never touched.
	resp = do(t, s, http.MethodGet, "/v1/jobs/res-job-b", ceilTokPlain, nil)
	var other job.JobResult
	decode(t, resp, &other)
	if other.ResultDecision != "" {
		t.Fatalf("job B result_decision = %q, want empty", other.ResultDecision)
	}
}
