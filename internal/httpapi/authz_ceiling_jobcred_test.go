package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// Job credentials under a ceiling (plan S5): the credential of a job inherits the ceiling of
// the job's ceiling owner (origin_caller), on exactly the routes SEC-01 already opens.

// seedCredentialJob upserts a running job of `project` whose ceiling owner is `origin`, and
// returns a member credential for it.
func seedCredentialJob(t *testing.T, s *Server, id, project, origin string) string {
	t.Helper()
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: id, ProjectKey: project, Agent: "exec", Runner: "worker", Status: job.StatusRunning,
		Cwd: ".", StartedAt: now, UpdatedAt: now, CallerID: origin, OriginCaller: origin,
	}); err != nil {
		t.Fatalf("seed credential job: %v", err)
	}
	return seedJobToken(t, s, id, jobstore.JobCredentialMember, "")
}

// jobCredentialRoutes is the job-credential route set, derived from the SEC-01 tables: every
// GET (a job credential may read every GET) and every route of jobWriteAllowlist. Each
// allowlist key must be matched by a declared route, so the set cannot silently shrink.
func jobCredentialRoutes(t *testing.T) []string {
	t.Helper()
	var keys []string
	matched := map[string]bool{}
	for _, key := range sortedRouteKeys() {
		decl := routeDecls[key]
		method, _, _ := strings.Cut(key, " ")
		inAllow := decl.jobCred != "" && jobWriteAllowlist[decl.jobCred]
		if inAllow {
			matched[decl.jobCred] = true
		}
		if decl.user.kind == userNone {
			continue
		}
		if methodIsRead(method) || inAllow {
			keys = append(keys, key)
		}
	}
	for allow := range jobWriteAllowlist {
		if !matched[allow] {
			t.Errorf("jobWriteAllowlist route %q is carried by no routeDecls entry (jobCred): the job-credential matrix cannot cover it", allow)
		}
	}
	return keys
}

// TestJobCredentialCeilingMatrix: a job whose ceiling owner is restricted to p1 reads and
// writes nothing of p2 through its credential, across the whole job-credential route set.
func TestJobCredentialCeilingMatrix(t *testing.T) {
	s := newCeilingServer(t)
	in := seedCeilingWorld(t, s, "p1")
	out := seedCeilingWorld(t, s, "p2")
	grantAll(s)
	tok := seedCredentialJob(t, s, "cred-job", "p1", "lim")
	_ = in

	routes := jobCredentialRoutes(t)
	var asserted int
	for _, key := range routes {
		decl := routeDecls[key]
		method, path, body := out.requestFor(key, decl)
		status, resp := ceilReq(t, s, tok, method, path, body)
		write := decl.effectiveRW(key) == rwWrite
		switch decl.scope.kind {
		case scopePathProject, scopeResource, scopeBodyProject:
			want := http.StatusNotFound
			if status == http.StatusForbidden && strings.Contains(resp, "may not") {
				continue // refused earlier by the SEC-01 table itself (steward / target-level rules)
			}
			if decl.scope.kind == scopeResource && decl.scope.arg == "worker" && write {
				want = http.StatusForbidden
			}
			if status != want {
				t.Errorf("%s: job credential, out-of-scope request %s %s = %d (%s), want %d", key, method, path, status, resp, want)
			}
			asserted++
		case scopeFilter:
			if _, ok := unfilteredAggregates[key]; ok {
				continue
			}
			for _, id := range out.ids() {
				if strings.Contains(resp, id) {
					t.Errorf("%s: job credential list reveals %q of p2: %s", key, id, resp)
				}
			}
			asserted++
		case scopeNoProject:
			if decl.user.kind == userRequire {
				if status != http.StatusForbidden {
					t.Errorf("%s: permission-gated server-global route = %d (%s), want 403 for a restricted job", key, status, resp)
				}
				asserted++
			}
		}
	}
	if asserted < 100 {
		t.Errorf("job-credential matrix asserted only %d of %d routes", asserted, len(routes))
	}
	t.Logf("job-credential matrix: %d of %d routes asserted", asserted, len(routes))
}

// TestJobCredentialInScopeAndUnrestricted: the restricted job still reads its own project; a
// job of an unrestricted owner (or of no recorded owner) is unchanged.
func TestJobCredentialInScopeAndUnrestricted(t *testing.T) {
	s := newCeilingServer(t)
	in := seedCeilingWorld(t, s, "p1")
	out := seedCeilingWorld(t, s, "p2")
	grantAll(s)
	restricted := seedCredentialJob(t, s, "cred-lim", "p1", "lim")
	plain := seedCredentialJob(t, s, "cred-plain", "p2", "plain")
	ownerless := seedCredentialJob(t, s, "cred-none", "p2", "")

	if status, body := ceilReq(t, s, restricted, http.MethodGet, "/v1/jobs/"+in.jobID, nil); status != http.StatusOK {
		t.Errorf("restricted job reading its own project: %d %s", status, body)
	}
	if status, body := ceilReq(t, s, restricted, http.MethodGet, "/v1/jobs/"+out.jobID, nil); status != http.StatusNotFound {
		t.Errorf("restricted job reading p2: %d %s, want 404", status, body)
	}
	for name, tok := range map[string]string{"unrestricted owner": plain, "no owner": ownerless} {
		for _, path := range []string{"/v1/jobs/" + out.jobID, "/v1/plans/" + out.planID, "/v1/projects/p2", "/v1/work-items/" + out.workItemID} {
			if status, body := ceilReq(t, s, tok, http.MethodGet, path, nil); status != http.StatusOK {
				t.Errorf("%s: GET %s = %d (%s), want 200", name, path, status, body)
			}
		}
	}
	// A global (plan-less) ask_human of a restricted job is its own question, not out of scope.
	if status, body := ceilReq(t, s, restricted, http.MethodPost, "/v1/decisions", map[string]any{"title": "t", "question": "q"}); status != http.StatusOK {
		t.Errorf("ask_human without a plan from a restricted job: %d %s", status, body)
	}
	// The SEC-01 write allowlist did not widen: a route outside it is still refused.
	status, body := ceilReq(t, s, restricted, http.MethodDelete, "/v1/jobs/"+in.jobID, nil)
	if status != http.StatusForbidden || !strings.Contains(body, "job credential may not") {
		t.Errorf("DELETE job by a job credential = %d (%s), want the SEC-01 refusal", status, body)
	}
}

// TestJobCredentialWithoutJobRowIsRefused: a credential whose job row is gone cannot be
// attributed to an owner, so it is refused (fail closed) on every route.
func TestJobCredentialWithoutJobRowIsRefused(t *testing.T) {
	s := newCeilingServer(t)
	seedCeilingWorld(t, s, "p1")
	grantAll(s)
	tok := seedJobToken(t, s, "ghost-job", jobstore.JobCredentialMember, "")
	for _, path := range []string{"/v1/jobs", "/v1/meta", "/v1/projects/p1"} {
		status, body := ceilReq(t, s, tok, http.MethodGet, path, nil)
		if status != http.StatusForbidden || !strings.Contains(body, "caller ceiling") {
			t.Errorf("GET %s with a credential of a missing job = %d (%s), want the ceiling 403", path, status, body)
		}
	}
}
