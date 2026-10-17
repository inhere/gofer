package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestPlanUsageCacheAndJobListUsageHTTP: the plan usage view carries cache totals (also
// per agent, with a resume carrier booked on its real agent), and listing jobs by plan
// returns each job's usage object.
func TestPlanUsageCacheAndJobListUsageHTTP(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	mkContinuePlan(t, s, "plan-cuse")
	for _, rec := range []jobstore.JobRecord{
		{ID: "cu-1", ProjectKey: "self", Agent: "claude", Runner: "local", Status: job.StatusDone,
			ResultDir: "/tmp/results/cu-1", StartedAt: 1, UpdatedAt: 1, EndedAt: 1,
			UsageJSON: `{"total_tokens":1000,"cache_read_tokens":300,"cache_write_tokens":40}`},
		{ID: "cu-2", ProjectKey: "self", Agent: "exec", ResumeAgent: "claude", Runner: "local", Status: job.StatusDone,
			ResultDir: "/tmp/results/cu-2", StartedAt: 2, UpdatedAt: 2, EndedAt: 2,
			UsageJSON: `{"total_tokens":200,"cache_read_tokens":100,"cache_write_tokens":5}`},
	} {
		if err := s.jobs.Meta().UpsertJob(rec); err != nil {
			t.Fatalf("upsert %s: %v", rec.ID, err)
		}
		resp := do(t, s, http.MethodPost, "/v1/plans/plan-cuse/jobs", testToken, map[string]string{"job_id": rec.ID})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("attach %s status=%d", rec.ID, resp.StatusCode)
		}
		resp.Body.Close()
	}

	resp := do(t, s, http.MethodGet, "/v1/plans/plan-cuse", testToken, nil)
	var detail struct {
		Usage struct {
			CacheRead  int64 `json:"cache_read_tokens"`
			CacheWrite int64 `json:"cache_write_tokens"`
			ByAgent    map[string]struct {
				Jobs       int   `json:"jobs"`
				CacheRead  int64 `json:"cache_read_tokens"`
				CacheWrite int64 `json:"cache_write_tokens"`
			} `json:"by_agent"`
		} `json:"usage"`
	}
	decode(t, resp, &detail)
	if detail.Usage.CacheRead != 400 || detail.Usage.CacheWrite != 45 {
		t.Fatalf("usage cache = %d/%d, want 400/45", detail.Usage.CacheRead, detail.Usage.CacheWrite)
	}
	cl := detail.Usage.ByAgent["claude"]
	if cl.Jobs != 2 || cl.CacheRead != 400 || cl.CacheWrite != 45 {
		t.Fatalf("claude usage = %+v, want the carrier folded in", cl)
	}
	if _, ok := detail.Usage.ByAgent["exec"]; ok {
		t.Fatalf("carrier must not be booked on exec: %+v", detail.Usage.ByAgent)
	}

	resp = do(t, s, http.MethodGet, "/v1/jobs?plan=plan-cuse", testToken, nil)
	var list struct {
		Jobs []struct {
			ID    string `json:"id"`
			Usage *struct {
				TotalTokens     int64 `json:"total_tokens"`
				CacheReadTokens int64 `json:"cache_read_tokens"`
			} `json:"usage"`
		} `json:"jobs"`
	}
	decode(t, resp, &list)
	if len(list.Jobs) != 2 {
		t.Fatalf("job list by plan = %d jobs, want 2", len(list.Jobs))
	}
	for _, j := range list.Jobs {
		if j.Usage == nil || j.Usage.TotalTokens == 0 || j.Usage.CacheReadTokens == 0 {
			t.Fatalf("job %s lacks usage in the plan-filtered list: %+v", j.ID, j.Usage)
		}
	}
}
