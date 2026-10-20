package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// 调用方上限第二期 E5 回归（gofer-7798）。

// TestCeilingJobsListKeepsWorkerOnlyProject: a glob ceiling is expanded to the literal projects the
// query is pushed down with — and that expansion MUST include the projects only a worker serves. A
// job of such a project (no `projects:` entry in the server config) is in scope for `p*`, so it has
// to be in the list, and the page is still cut AFTER the filter (E5 regression, gofer-7798).
func TestCeilingJobsListKeepsWorkerOnlyProject(t *testing.T) {
	t.Parallel()
	s := recordingServerWith(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "mul", Token: "tok-mul", Ceiling: &config.CallerCeiling{Projects: []string{"p*"}}},
			{ID: "plain", Token: ceilTokPlain},
		},
		Workers: map[string]config.WorkerAuthConfig{"w-wonly": {Token: "tok-w"}},
	}, func(cfg *config.Config) {
		for _, p := range []string{"p1", "p2"} {
			cfg.Projects[p] = config.ProjectConfig{
				HostPath: cfg.Storage.Root, AllowedAgents: []string{"exec"},
				AllowedRunners: []string{"local"}, AllowExec: true,
			}
		}
	})
	grantAll(s)
	// p9 exists only on the worker: it is not a declared server project.
	s.workers = fakeWorkers{"w-wonly": {Connected: true, Projects: []string{"p1", "p9"}}}
	s.router = s.buildRouter()

	now := time.Now().Unix()
	seedListJob(t, s, "j-out", "self", now) // newest, outside "p*"
	seedListJob(t, s, "j-p9", "p9", now-1)  // worker-only project, inside "p*"
	seedListJob(t, s, "j-p1", "p1", now-2)

	pageIDs := func(token, query string) []string {
		t.Helper()
		status, body := ceilReq(t, s, token, http.MethodGet, "/v1/jobs"+query, nil)
		if status != http.StatusOK {
			t.Fatalf("GET /v1/jobs%s as %s = %d: %s", query, token, status, body)
		}
		return jsonNames(decodeJSON(t, body)["jobs"], "id")
	}

	// A page of 2 is full of in-scope rows: the out-of-scope job of `self` must not eat a slot,
	// and the worker-only job must be there.
	got := pageIDs("tok-mul", "?limit=2")
	if len(got) != 2 {
		t.Fatalf("restricted page = %v, want 2 in-scope jobs", got)
	}
	if !hasID(got, "j-p9") {
		t.Errorf("restricted page = %v, want the job of the worker-only project p9", got)
	}
	if hasID(got, "j-out") {
		t.Errorf("restricted page = %v, want no job outside the ceiling", got)
	}

	if all := pageIDs(ceilTokPlain, "?limit=10"); len(all) != 3 {
		t.Errorf("unrestricted list = %v, want every job", all)
	}
}

// hasID reports whether ids holds want.
func hasID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
