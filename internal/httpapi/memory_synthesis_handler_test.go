package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// Reads of daily memory synthesis runs follow the other memory read routes (Public + project
// scope Filter): the list is project filtered and metadata only, the detail respects the
// ceiling, and a job credential never receives the stored material snapshot.
func TestMemorySynthesisReadEndpoints(t *testing.T) {
	t.Parallel()
	s := recordingServerWith(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "lim", Token: ceilTokLim, Ceiling: &config.CallerCeiling{Projects: []string{"p1"}}},
			{ID: "plain", Token: ceilTokPlain},
		},
	}, func(cfg *config.Config) {
		for _, p := range []string{"p1", "p2"} {
			cfg.Projects[p] = config.ProjectConfig{HostPath: cfg.Storage.Root, AllowedAgents: []string{"exec"}, AllowedRunners: []string{"local"}, AllowExec: true}
		}
	})
	grantAll(s)
	meta := s.jobs.Meta()
	for _, r := range []jobstore.MemorySynthesisRun{
		{ID: "ms-p1", ProjectKey: "p1", Day: "2026-10-28", Status: "succeeded", InputDigest: "d1", MaterialSchema: "daily-memory-materials.v1",
			StartedAt: 10, ResultJSON: `{"summary":"RESULT-P1"}`, MaterialSnapshot: `{"items":"SNAP-P1"}`},
		{ID: "ms-p2", ProjectKey: "p2", Day: "2026-10-28", Status: "failed", InputDigest: "d2", MaterialSchema: "daily-memory-materials.v1",
			StartedAt: 20, MaterialSnapshot: `{"items":"SNAP-P2"}`},
	} {
		if err := meta.InsertMemorySynthesisRun(r); err != nil {
			t.Fatal(err)
		}
	}
	runIDs := func(body string) []string {
		var out struct {
			Runs []jobstore.MemorySynthesisRun `json:"runs"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
		ids := []string{}
		for _, r := range out.Runs {
			if r.ResultJSON != "" || r.MaterialSnapshot != "" {
				t.Errorf("list must be metadata only: %+v", r)
			}
			ids = append(ids, r.ID)
		}
		return ids
	}

	// Unrestricted caller: both projects, filterable by ?project and ?state.
	status, body := ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/memory-syntheses", nil)
	if status != http.StatusOK || len(runIDs(body)) != 2 {
		t.Fatalf("plain list = %d %s", status, body)
	}
	status, body = ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/memory-syntheses?project=p2", nil)
	if ids := runIDs(body); status != http.StatusOK || len(ids) != 1 || ids[0] != "ms-p2" {
		t.Fatalf("project filter = %d %s", status, body)
	}
	status, body = ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/memory-syntheses?state=succeeded", nil)
	if ids := runIDs(body); status != http.StatusOK || len(ids) != 1 || ids[0] != "ms-p1" {
		t.Fatalf("state filter = %d %s", status, body)
	}

	// A restricted caller only sees its project, even when it asks for another one.
	status, body = ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/memory-syntheses", nil)
	if ids := runIDs(body); status != http.StatusOK || len(ids) != 1 || ids[0] != "ms-p1" {
		t.Fatalf("restricted list = %d %s", status, body)
	}
	status, body = ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/memory-syntheses?project=p2", nil)
	if ids := runIDs(body); status != http.StatusOK || len(ids) != 0 {
		t.Fatalf("restricted foreign project = %d %s", status, body)
	}

	// Detail: result and snapshot for a user in scope; out of scope is refused.
	status, body = ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/memory-syntheses/ms-p1", nil)
	var one jobstore.MemorySynthesisRun
	_ = json.Unmarshal([]byte(body), &one)
	if status != http.StatusOK || one.MaterialSnapshot == "" || one.ResultJSON == "" {
		t.Fatalf("detail = %d %s", status, body)
	}
	if status, body = ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/memory-syntheses/ms-p2", nil); status == http.StatusOK {
		t.Fatalf("out-of-scope detail must be refused: %d %s", status, body)
	}
	if status, _ = ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/memory-syntheses/nope", nil); status != http.StatusNotFound {
		t.Fatalf("missing detail = %d", status)
	}
}

// A job credential may read the metadata (and the result) but never the material snapshot.
func TestMemorySynthesisDetailHidesSnapshotFromJobCredential(t *testing.T) {
	s := newTestServer(t, testToken, false)
	if err := s.jobs.Meta().InsertMemorySynthesisRun(jobstore.MemorySynthesisRun{ID: "ms-1", ProjectKey: "self", Day: "2026-10-28",
		Status: "succeeded", InputDigest: "d", MaterialSchema: "daily-memory-materials.v1", StartedAt: 1,
		ResultJSON: `{"summary":"ok"}`, MaterialSnapshot: `{"items":"SNAP"}`}); err != nil {
		t.Fatal(err)
	}
	member := seedJobToken(t, s, "job-syn-member", jobstore.JobCredentialMember, "")
	var asJob, asUser jobstore.MemorySynthesisRun
	todayCall(t, s, http.MethodGet, "/v1/memory-syntheses/ms-1", member, nil, 200, &asJob)
	todayCall(t, s, http.MethodGet, "/v1/memory-syntheses/ms-1", testToken, nil, 200, &asUser)
	if asJob.MaterialSnapshot != "" || asJob.ResultJSON == "" || asJob.ID != "ms-1" {
		t.Fatalf("job credential view: %+v", asJob)
	}
	if asUser.MaterialSnapshot == "" {
		t.Fatalf("user view must carry the snapshot: %+v", asUser)
	}
	var list struct {
		Runs []jobstore.MemorySynthesisRun `json:"runs"`
	}
	todayCall(t, s, http.MethodGet, "/v1/memory-syntheses", member, nil, 200, &list)
	if len(list.Runs) != 1 || list.Runs[0].MaterialSnapshot != "" || list.Runs[0].ResultJSON != "" {
		t.Fatalf("job credential list: %+v", list.Runs)
	}
}
