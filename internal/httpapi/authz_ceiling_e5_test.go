package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// 调用方上限第二期 E5：jobs 列表的多项目上限（gofer-7798，计划 E5）。
//
// 一个通配符上限（projects: [p*]）先展开成本 server 上的字面项目，再作为多项目 IN 条件下推到
// 查询，limit / offset 因此在范围内分页；未受限调用方拿到全部。

// seedListJob upserts one done job started at shifted seconds, so the list order is known.
func seedListJob(t *testing.T, s *Server, id, project string, startedAt int64) {
	t.Helper()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: id, ProjectKey: project, Agent: "exec", Runner: "local", Status: job.StatusDone,
		Cwd: ".", ResultDir: t.TempDir(), StartedAt: startedAt, EndedAt: startedAt, UpdatedAt: startedAt,
		CallerID: ceilOwner,
	}); err != nil {
		t.Fatalf("seed job %s (%s): %v", id, project, err)
	}
}

// TestCeilingJobsListPaginatesInScope: a multi-project (glob) ceiling is pushed into the query, so
// page one is full of in-scope rows and page two continues where it ended; the unrestricted caller
// still sees everything.
func TestCeilingJobsListPaginatesInScope(t *testing.T) {
	t.Parallel()
	s := recordingServerWith(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "mul", Token: "tok-mul", Ceiling: &config.CallerCeiling{Projects: []string{"p*"}}},
			{ID: "plain", Token: ceilTokPlain},
		},
		Governance: config.GovernanceConfig{
			RequireAnswerCapability: true, RequireAdminCapability: true,
			RequireAttachCapability: true, RequireTunnelCapability: true,
		},
	}, func(cfg *config.Config) {
		for _, p := range []string{"p1", "p2"} {
			cfg.Projects[p] = config.ProjectConfig{
				HostPath: cfg.Storage.Root, AllowedAgents: []string{"exec"},
				AllowedRunners: []string{"local"}, AllowExec: true,
			}
		}
	})
	grantAll(s)
	now := time.Now().Unix()
	// Three NEWEST jobs outside the ceiling (the default `self` project, which "p*" does not admit),
	// then the in-scope ones: p1 (2 jobs) and p2 (1 job).
	seedListJob(t, s, "j-self-3", "self", now)
	seedListJob(t, s, "j-self-2", "self", now-1)
	seedListJob(t, s, "j-self-1", "self", now-2)
	seedListJob(t, s, "j-p1-2", "p1", now-3)
	seedListJob(t, s, "j-p2-1", "p2", now-4)
	seedListJob(t, s, "j-p1-1", "p1", now-5)

	pageIDs := func(token, query string) []string {
		t.Helper()
		status, body := ceilReq(t, s, token, http.MethodGet, "/v1/jobs"+query, nil)
		if status != http.StatusOK {
			t.Fatalf("GET /v1/jobs%s as %s = %d: %s", query, token, status, body)
		}
		return jsonNames(decodeJSON(t, body)["jobs"], "id")
	}

	// Page one is cut AFTER the project filter: it is full (2) and holds no out-of-scope job, even
	// though the three newest jobs of the server belong to `self`.
	first := pageIDs("tok-mul", "?limit=2")
	if len(first) != 2 {
		t.Fatalf("restricted page 1 = %v, want a full page of 2 in-scope jobs", first)
	}
	for _, id := range first {
		if strings.Contains(id, "self") {
			t.Errorf("restricted page 1 = %v, want no job of the out-of-scope project", first)
		}
	}
	second := pageIDs("tok-mul", "?limit=2&offset=2")
	if len(second) != 1 {
		t.Fatalf("restricted page 2 = %v, want the remaining in-scope job", second)
	}
	seen := map[string]bool{}
	for _, id := range append(append([]string{}, first...), second...) {
		seen[id] = true
	}
	for _, want := range []string{"j-p1-2", "j-p2-1", "j-p1-1"} {
		if !seen[want] {
			t.Errorf("restricted pages = %v, want %s", seen, want)
		}
	}
	if len(seen) != 3 {
		t.Errorf("restricted pages covered %v, want exactly the 3 in-scope jobs", seen)
	}

	if all := pageIDs(ceilTokPlain, "?limit=10"); len(all) != 6 {
		t.Errorf("unrestricted list = %v, want every job", all)
	}
}
