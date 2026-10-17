package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// permissionOnlyServer is a review server whose users derive no permission from the
// config (no root administrator, every governance gate open): the database grants set per
// test are the whole of a caller's authority.
func permissionOnlyServer(t *testing.T) *Server {
	t.Helper()
	return newReviewServer(t, config.ServerConfig{
		Token: testToken,
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
		},
		Governance: config.GovernanceConfig{
			RequireAnswerCapability: true, RequireAdminCapability: true,
			RequireAttachCapability: true, RequireTunnelCapability: true,
		},
	})
}

func countJobs(t *testing.T, s *Server) int {
	t.Helper()
	list, err := s.jobs.ListJobs(job.ListOpts{All: true, Limit: 1000})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	return len(list)
}

// TestRejectResumeRequiresJobSubmit (gofer-dc9l): `reject?resume=true` starts a NEW job, so
// a caller holding job.review but not job.submit is refused with a 403 naming job.submit,
// no job is created and the rejection is not recorded; a plain reject (no resume) still
// works with job.review alone, and with both permissions the resume goes through.
func TestRejectResumeRequiresJobSubmit(t *testing.T) {
	// Not parallel: setDBGrants swaps the server's grant cache.
	s := permissionOnlyServer(t)
	s.setDBGrants(grantsFor([]string{authz.PermJobSubmit}))
	first := submitReviewJob(t, s, "tok-alice")
	before := countJobs(t, s)

	s.setDBGrants(grantsFor([]string{authz.PermJobReview}))
	resp := do(t, s, http.MethodPost, "/v1/jobs/"+first.ID+"/reject", "tok-alice",
		map[string]any{"note": "redo it", "resume": true})
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reject?resume without job.submit = %d, want 403 (body %s)", resp.StatusCode, body)
	}
	if !strings.Contains(body, authz.PermJobSubmit) {
		t.Errorf("403 body %q does not name %s", body, authz.PermJobSubmit)
	}
	if got := countJobs(t, s); got != before {
		t.Fatalf("a refused resume created a job: %d jobs, want %d", got, before)
	}
	if res, _ := s.jobs.Get(first.ID); res.Status != job.StatusNeedsReview {
		t.Fatalf("a refused reject?resume changed the job to %s, want needs_review", res.Status)
	}

	// Both permissions: the resume starts the continuation.
	s.setDBGrants(grantsFor([]string{authz.PermJobReview, authz.PermJobSubmit}))
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+first.ID+"/reject", "tok-alice",
		map[string]any{"note": "redo it", "resume": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reject?resume with review+submit = %d, want 200 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	var out job.ReviewOutcome
	decode(t, resp, &out)
	if out.ResumeJobID == "" {
		t.Fatalf("reject?resume with review+submit started no continuation: %+v", out)
	}
}

// TestRejectWithoutResumeNeedsOnlyJobReview: the job.submit requirement is only for the
// resume; a plain reject stays a pure review decision.
func TestRejectWithoutResumeNeedsOnlyJobReview(t *testing.T) {
	s := permissionOnlyServer(t)
	s.setDBGrants(grantsFor([]string{authz.PermJobSubmit}))
	first := submitReviewJob(t, s, "tok-alice")

	s.setDBGrants(grantsFor([]string{authz.PermJobReview}))
	resp := do(t, s, http.MethodPost, "/v1/jobs/"+first.ID+"/reject", "tok-alice",
		map[string]any{"note": "no"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("plain reject with job.review = %d, want 200 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()

	// Without job.review even a plain reject is refused.
	s.setDBGrants(grantsFor([]string{authz.PermJobSubmit}))
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+first.ID+"/reject", "tok-alice",
		map[string]any{"note": "no"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reject without job.review = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestSessionJobDeniedWritesSingleResponse (gofer-dc9l): a user lacking job.manage.others
// who speaks into someone else's session job gets exactly one well-formed 403 body (the
// old code wrote the permission-denied body and then a second error body behind it).
func TestSessionJobDeniedWritesSingleResponse(t *testing.T) {
	s := permissionOnlyServer(t)
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: "job-bob-session", ProjectKey: "self", Agent: "exec", Runner: "worker",
		Status: job.StatusDone, Cwd: ".", StartedAt: now, EndedAt: now, UpdatedAt: now, CallerID: "bob",
	}); err != nil {
		t.Fatalf("upsert job: %v", err)
	}
	s.setDBGrants(nil)

	for _, path := range []string{"/v1/jobs/job-bob-session/say", "/v1/jobs/job-bob-session/end"} {
		resp := do(t, s, http.MethodPost, path, "tok-alice", map[string]string{"message": "hi"})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s = %d, want 403", path, resp.StatusCode)
		}
		dec := json.NewDecoder(resp.Body)
		var first errorBody
		if err := dec.Decode(&first); err != nil {
			t.Fatalf("%s: decode first body: %v", path, err)
		}
		if !strings.Contains(first.Error, authz.PermJobManageOthers) {
			t.Errorf("%s: error %q does not name %s", path, first.Error, authz.PermJobManageOthers)
		}
		var extra json.RawMessage
		if err := dec.Decode(&extra); err != io.EOF {
			t.Errorf("%s: a second response body follows the 403 (%s, err=%v), want exactly one", path, extra, err)
		}
		resp.Body.Close()
	}
}
