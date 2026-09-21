package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// TestJobsCountWaitingDirAsQueued: `waiting_dir`（JOB-11：等在同一个目录锁上）是一个
// "排队中"的非终态，`/v1/stats` 的 by_status 必须把它并进 queued——否则首页会多出一个
// 没人认识的桶，而"排队等锁"看起来像什么都没发生。
func TestJobsCountWaitingDirAsQueued(t *testing.T) {
	s := newTestServer(t, testToken, false)
	meta := s.jobs.Meta()
	for _, rec := range []struct{ id, status string }{
		{"job-wait", job.StatusWaitingDir},
		{"job-running", job.StatusRunning},
	} {
		if err := meta.UpsertJob(statsJobRecord(rec.id, rec.status, 100)); err != nil {
			t.Fatalf("upsert %s: %v", rec.id, err)
		}
	}

	resp := do(t, s, http.MethodGet, "/v1/stats", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats status=%d, want 200", resp.StatusCode)
	}
	var body statsResp
	decode(t, resp, &body)

	if got := body.Jobs.ByStatus[job.StatusQueued]; got != 1 {
		t.Fatalf("by_status[queued] = %d, want 1 (the waiting_dir job): %+v", got, body.Jobs.ByStatus)
	}
	if _, ok := body.Jobs.ByStatus[job.StatusWaitingDir]; ok {
		t.Fatalf("waiting_dir must be folded into queued, got its own bucket: %+v", body.Jobs.ByStatus)
	}
	if body.Jobs.Total != 2 {
		t.Fatalf("jobs.total = %d, want 2", body.Jobs.Total)
	}
}
