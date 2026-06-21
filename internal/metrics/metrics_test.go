package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// scrapeBody serves m.Handler() once and returns the exposition text.
func scrapeBody(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handler status=%d, want 200", rec.Code)
	}
	b, _ := io.ReadAll(rec.Result().Body)
	return string(b)
}

// TestNewRegistersAllFamilies asserts the registry serves the Go runtime + the
// gofer collector families after recording one of each kind.
func TestNewRegistersAllFamilies(t *testing.T) {
	m := New()
	m.JobSubmitted("ci", "proj", "exec", "local")
	m.JobTerminal("done", "ci", "proj", "exec", "local", 1.5)
	m.ObserveHTTP("GET", "/v1/jobs/:id", "200", 0.01)
	m.RegisterRuntimeGauges(
		func() (int, int, int) { return 2, 1, 1 },
		func() (int, int) { return 3, 4 },
	)

	body := scrapeBody(t, m)
	for _, want := range []string{
		"go_goroutines",
		"gofer_http_requests_total",
		"gofer_http_request_duration_seconds_bucket",
		"gofer_jobs_submitted_total",
		"gofer_jobs_terminal_total",
		"gofer_job_duration_seconds_bucket",
		"gofer_jobs_in_flight",
		"gofer_jobs_queued",
		"gofer_jobs_running",
		"gofer_workers_connected",
		"gofer_worker_in_flight",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("scrape missing %q\n%s", want, body)
		}
	}
}

// TestGaugeFuncReadsLiveStateAtScrape asserts the gauges reflect whatever the
// callbacks return at scrape time (no cached/periodic value).
func TestGaugeFuncReadsLiveStateAtScrape(t *testing.T) {
	m := New()
	inflight, queued, running := 5, 2, 3
	connected, wInflight := 7, 9
	m.RegisterRuntimeGauges(
		func() (int, int, int) { return inflight, queued, running },
		func() (int, int) { return connected, wInflight },
	)
	body := scrapeBody(t, m)
	for series, want := range map[string]string{
		"gofer_jobs_in_flight":    "5",
		"gofer_jobs_queued":       "2",
		"gofer_jobs_running":      "3",
		"gofer_workers_connected": "7",
		"gofer_worker_in_flight":  "9",
	} {
		if !strings.Contains(body, series+" "+want) {
			t.Fatalf("gauge %s should read %s at scrape time\n%s", series, want, body)
		}
	}
}

// TestEmptyCallerLabelledAnon asserts an empty caller id becomes the "anon" label
// (no empty-string label values).
func TestEmptyCallerLabelledAnon(t *testing.T) {
	m := New()
	m.JobSubmitted("", "proj", "exec", "local")
	body := scrapeBody(t, m)
	if !strings.Contains(body, `caller="anon"`) {
		t.Fatalf("empty caller not labelled anon\n%s", body)
	}
}

// TestNilMetricsIsNoOp asserts every record method is a safe no-op on a nil
// *Metrics (the "metrics disabled" sentinel used by non-serve callers/tests).
func TestNilMetricsIsNoOp(t *testing.T) {
	var m *Metrics
	// None of these should panic.
	m.JobSubmitted("c", "p", "a", "r")
	m.JobTerminal("done", "c", "p", "a", "r", 1)
	m.ObserveHTTP("GET", "/x", "200", 0.1)
	m.RegisterRuntimeGauges(
		func() (int, int, int) { return 0, 0, 0 },
		func() (int, int) { return 0, 0 },
	)
}
