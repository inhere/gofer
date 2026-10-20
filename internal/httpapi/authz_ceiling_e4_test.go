package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// 调用方上限第二期 E4：审阅（accept / reject / approve，gofer-7798，计划 E4、决策 6）。
//
//   - 审阅自己的 job 只需 job.review；审阅他人的 job 还需要 job.review.others；
//   - 受限调用方不能审阅「上限所有者（OriginCaller）是自己」的 job；
//   - 未受限调用方保持现状（自己的 job 只需 job.review，他人的 job 需 .others）。

// seedCeilingReviewJob upserts one reviewable / approvable job with the caller (submitter) and
// the ceiling owner (OriginCaller) the case needs.
func seedCeilingReviewJob(t *testing.T, s *Server, id, project, caller, origin, status string) {
	t.Helper()
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: id, ProjectKey: project, Agent: "claude", Runner: "local", Status: status,
		Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, EndedAt: now, UpdatedAt: now,
		CallerID: caller, OriginCaller: origin,
	}); err != nil {
		t.Fatalf("seed job %s: %v", id, err)
	}
}

// TestCeilingReviewMatrix walks accept / reject / approve over the review cases: own vs others,
// with and without job.review.others, restricted vs unrestricted, and a restricted caller's own
// ceiling owning the job. A permitted case must not answer 403 (the orchestration's own state
// answer — 404 / 409 — is not the permission's business).
func TestCeilingReviewMatrix(t *testing.T) {
	t.Parallel()
	s := newCeilingServer(t)
	// lim is the restricted caller (ceiling projects [p1]), plain has no ceiling.
	seedCeilingReviewJob(t, s, "own-free", "p1", "lim", "", job.StatusNeedsReview)
	seedCeilingReviewJob(t, s, "own-owned", "p1", "lim", "lim", job.StatusNeedsReview)
	seedCeilingReviewJob(t, s, "other", "p1", "bob", "bob", job.StatusNeedsReview)
	seedCeilingReviewJob(t, s, "other-owned", "p1", "bob", "lim", job.StatusNeedsReview)
	seedCeilingReviewJob(t, s, "plain-own", "p1", "plain", "plain", job.StatusNeedsReview)

	review := []string{authz.PermJobReview}
	both := []string{authz.PermJobReview, authz.PermJobReviewOthers}
	cases := []struct {
		name      string
		token     string
		perms     []string
		jobID     string
		forbidden bool
		// missing is the permission the 403 body must name ("" = any 403).
		missing string
	}{
		{"own, job.review only", ceilTokLim, review, "own-free", false, ""},
		{"own, ceiling owner is the caller", ceilTokLim, both, "own-owned", true, ""},
		{"own, ceiling owner and job.review only", ceilTokLim, review, "own-owned", true, ""},
		{"other's, job.review only", ceilTokLim, review, "other", true, authz.PermJobReviewOthers},
		{"other's, with job.review.others", ceilTokLim, both, "other", false, ""},
		{"other's submitted, but the ceiling owner is the caller", ceilTokLim, both, "other-owned", true, ""},
		{"unrestricted own, job.review only", ceilTokPlain, review, "plain-own", false, ""},
		{"unrestricted other's, job.review only", ceilTokPlain, review, "other", true, authz.PermJobReviewOthers},
	}
	for _, action := range []string{"accept", "reject", "approve"} {
		for _, tc := range cases {
			s.setDBGrants(grantsFor(tc.perms))
			status, body := ceilReq(t, s, tc.token, http.MethodPost, "/v1/jobs/"+tc.jobID+"/"+action, map[string]any{})
			switch {
			case tc.forbidden && status != http.StatusForbidden:
				t.Errorf("%s (%s) = %d, want 403: %s", tc.name, action, status, body)
			case !tc.forbidden && status == http.StatusForbidden:
				t.Errorf("%s (%s) = 403, want the review to pass the permission: %s", tc.name, action, body)
			}
			if tc.forbidden && tc.missing != "" && !strings.Contains(body, tc.missing) {
				t.Errorf("%s (%s) 403 body = %s, want it to name %s", tc.name, action, body, tc.missing)
			}
		}
	}
}
