package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/overview"
)

// TestStatsOverviewEndpoint: GET /v1/stats/overview forwards range / tz to the overview
// service, answers 400 on a bad range or tz, and serves the second call from the cache.
func TestStatsOverviewEndpoint(t *testing.T) {
	s := newTestServer(t, testToken, false)
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{ID: "ov-1", ProjectKey: "self", Agent: "exec", Runner: "local",
		Status: job.StatusDone, ResultDir: t.TempDir(), StartedAt: now - 120, EndedAt: now - 60}); err != nil {
		t.Fatal(err)
	}

	resp := do(t, s, http.MethodGet, "/v1/stats/overview?range=7d&tz=480", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("overview status=%d, want 200", resp.StatusCode)
	}
	var body overview.Overview
	decode(t, resp, &body)
	if body.Range.Key != "7d" || body.Range.TZ != 480 || body.Jobs.Total != 1 || body.Jobs.Done != 1 || len(body.Daily) != 7 {
		t.Fatalf("overview body wrong: range=%+v jobs=%+v daily=%d", body.Range, body.Jobs, len(body.Daily))
	}
	if body.Cached {
		t.Fatal("first call must be a fresh build")
	}
	resp = do(t, s, http.MethodGet, "/v1/stats/overview?range=7d&tz=480", testToken, nil)
	decode(t, resp, &body)
	if !body.Cached {
		t.Fatal("second call inside the TTL must come from the cache")
	}

	// range=today carries the 24-row hourly series (its counts agree with the Jobs card);
	// an empty range resolves to 7d and has no hourly series.
	resp = do(t, s, http.MethodGet, "/v1/stats/overview?range=today&tz=480", testToken, nil)
	var today overview.Overview
	decode(t, resp, &today)
	hourDone := 0
	for _, h := range today.Hourly {
		hourDone += h.Done
	}
	if today.Range.Key != "today" || len(today.Hourly) != 24 || hourDone != today.Jobs.Done || len(today.Daily) != 1 {
		t.Fatalf("today body wrong: range=%+v hourly=%d done=%d/%d daily=%d", today.Range, len(today.Hourly), hourDone, today.Jobs.Done, len(today.Daily))
	}
	resp = do(t, s, http.MethodGet, "/v1/stats/overview?tz=480", testToken, nil)
	var def overview.Overview
	decode(t, resp, &def)
	if def.Range.Key != "7d" || def.Hourly != nil {
		t.Fatalf("default range=%q hourly=%d, want 7d without hourly", def.Range.Key, len(def.Hourly))
	}

	for _, path := range []string{"/v1/stats/overview?range=1y", "/v1/stats/overview?tz=abc", "/v1/stats/overview?tz=9999"} {
		if resp := do(t, s, http.MethodGet, path, testToken, nil); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s status=%d, want 400", path, resp.StatusCode)
		}
	}
}

// TestStatsBackfillEndpoint: POST /v1/stats/backfill computes the missing job_metrics
// rows and a second call has nothing left to do.
func TestStatsBackfillEndpoint(t *testing.T) {
	s := newTestServer(t, testToken, false)
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{ID: "bf-1", ProjectKey: "self", Agent: "exec", Runner: "local",
		Status: job.StatusDone, ResultDir: t.TempDir(), StartedAt: 100, EndedAt: 160}); err != nil {
		t.Fatal(err)
	}
	resp := do(t, s, http.MethodPost, "/v1/stats/backfill", testToken, map[string]any{"limit": 10})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("backfill status=%d, want 200", resp.StatusCode)
	}
	var res job.BackfillResult
	decode(t, resp, &res)
	if res.Written != 1 || !res.Done {
		t.Fatalf("backfill result=%+v, want 1 written and done", res)
	}
	if _, ok, err := s.jobs.Meta().GetJobMetrics("bf-1"); err != nil || !ok {
		t.Fatalf("metrics row missing: ok=%v err=%v", ok, err)
	}
	resp = do(t, s, http.MethodPost, "/v1/stats/backfill", testToken, nil)
	decode(t, resp, &res)
	if res.Scanned != 0 {
		t.Fatalf("second backfill scanned %d, want 0", res.Scanned)
	}
	if resp := do(t, s, http.MethodPost, "/v1/stats/backfill", testToken, map[string]any{"limit": 5000}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("limit 5000 status=%d, want 400", resp.StatusCode)
	}
}
