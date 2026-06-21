package httpapi

import (
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/metrics"
)

// newMetricsServer builds a test server (project "self", exec agent) with the E16
// metrics instrumentation wired: the registry is injected into both the job
// service (lifecycle counters) and the server (/metrics endpoint + /v1 HTTP
// middleware). enabled/token mirror the serverCfg.Metrics knobs.
func newMetricsServer(t *testing.T, enabled bool, token string) (*Server, *metrics.Metrics) {
	t.Helper()
	s := newTestServer(t, testToken, false)
	m := metrics.New()
	s.jobs.SetMetrics(m)
	m.RegisterRuntimeGauges(
		func() (int, int, int) { st := s.jobs.Stats(); return st.InFlight, st.Queued, st.Running },
		func() (int, int) { return 0, 0 }, // no hub in tests
	)
	s.SetMetrics(m, enabled, token)
	return s, m
}

// scrape performs an in-process GET /metrics and returns the body. token is the
// Bearer presented (empty = no header).
func scrape(t *testing.T, s *Server, token string) (*http.Response, string) {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/metrics", token, nil)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// TestMetricsEndpointExposesRuntimeAndGoMetrics asserts the scrape includes the
// Go runtime collector + the gofer gauge families even before any job runs.
func TestMetricsEndpointExposesRuntimeAndGoMetrics(t *testing.T) {
	s, _ := newMetricsServer(t, true, "")
	resp, body := scrape(t, s, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status=%d, want 200 (unauthenticated)", resp.StatusCode)
	}
	for _, want := range []string{
		"go_goroutines",
		"gofer_jobs_in_flight",
		"gofer_jobs_queued",
		"gofer_workers_connected",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("scrape missing metric %q\n---\n%s", want, body)
		}
	}
}

// TestMetricsJobCounters submits N exec jobs, waits for terminal, then asserts the
// submitted / terminal{done} counters reflect the run.
func TestMetricsJobCounters(t *testing.T) {
	s, _ := newMetricsServer(t, true, "")

	const n = 3
	var ids []string
	for i := 0; i < n; i++ {
		resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("create #%d status=%d, want 200", i, resp.StatusCode)
		}
		var created job.JobResult
		decode(t, resp, &created)
		ids = append(ids, created.ID)
	}
	for _, id := range ids {
		final := waitDone(t, s, id)
		if final.Status != job.StatusDone {
			t.Fatalf("job %s status=%s, want done (err=%s)", id, final.Status, final.Error)
		}
	}

	_, body := scrape(t, s, "")

	// submitted counter: caller=default (token auth), project/agent/runner labels.
	gotSubmitted := metricValue(t, body, `gofer_jobs_submitted_total{`+
		`agent="exec",caller="default",project="self",runner="local"}`)
	if gotSubmitted < float64(n) {
		t.Fatalf("gofer_jobs_submitted_total=%v, want >= %d\n%s", gotSubmitted, n, sampleLines(body, "gofer_jobs_submitted_total"))
	}

	// terminal{status=done} counter must be present and >= n.
	gotDone := metricValue(t, body, `gofer_jobs_terminal_total{`+
		`caller="default",project="self",status="done"}`)
	if gotDone < float64(n) {
		t.Fatalf("gofer_jobs_terminal_total{status=done}=%v, want >= %d\n%s", gotDone, n, sampleLines(body, "gofer_jobs_terminal_total"))
	}

	// duration histogram must have observed at least n samples.
	gotDurCount := metricValue(t, body, `gofer_job_duration_seconds_count{`+
		`agent="exec",runner="local",status="done"}`)
	if gotDurCount < float64(n) {
		t.Fatalf("gofer_job_duration_seconds_count=%v, want >= %d\n%s", gotDurCount, n, sampleLines(body, "gofer_job_duration_seconds_count"))
	}
}

// TestMetricsHTTPRequestsLabelled asserts the HTTP middleware counts /v1 requests
// with a method/route/status label set.
func TestMetricsHTTPRequestsLabelled(t *testing.T) {
	s, _ := newMetricsServer(t, true, "")
	// A couple of /v1 GETs to populate the http counter.
	do(t, s, http.MethodGet, "/v1/projects", testToken, nil).Body.Close()
	do(t, s, http.MethodGet, "/v1/agents", testToken, nil).Body.Close()

	_, body := scrape(t, s, "")
	if !strings.Contains(body, "gofer_http_requests_total") {
		t.Fatalf("scrape missing gofer_http_requests_total\n%s", body)
	}
	// the /v1/projects GET must be counted with a 200 status label.
	if metricValue(t, body, `gofer_http_requests_total{method="GET",route="/v1/projects",status="200"}`) < 1 {
		t.Fatalf("missing /v1/projects counter\n%s", sampleLines(body, "gofer_http_requests_total"))
	}
}

// routeLabelRe extracts the route="..." label value from a sample line.
var routeLabelRe = regexp.MustCompile(`route="([^"]*)"`)

// knownRouteTemplates is the cardinality whitelist: every route label the metrics
// middleware may emit for the routes exercised by the integration tests. They are
// rux MATCHED route templates (colon-syntax placeholders), never raw paths — so a
// specific job id must NEVER appear as a route value.
var knownRouteTemplates = map[string]bool{
	"/v1/jobs":          true, // POST /v1/jobs + GET /v1/jobs
	"/v1/jobs/:id":      true, // GET /v1/jobs/{id}
	"/v1/projects":      true,
	"/v1/projects/:key": true,
	"/v1/agents":        true,
}

// TestMetricsRouteCardinalityGuard submits jobs (which adds /v1/jobs/{id} polls
// via waitDone) and asserts EVERY route label in the scrape is a bounded template
// from the whitelist — no raw path with an embedded job id ever leaks in.
func TestMetricsRouteCardinalityGuard(t *testing.T) {
	s, _ := newMetricsServer(t, true, "")

	// Submit + poll a job: waitDone issues several GET /v1/jobs/{id} requests, the
	// classic high-cardinality trap. The route label must collapse to /v1/jobs/:id.
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	var created job.JobResult
	decode(t, resp, &created)
	waitDone(t, s, created.ID)
	// Also hit a couple of static routes.
	do(t, s, http.MethodGet, "/v1/projects", testToken, nil).Body.Close()

	_, body := scrape(t, s, "")

	var routes []string
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "gofer_http_requests_total{") &&
			!strings.HasPrefix(line, "gofer_http_request_duration_seconds") {
			continue
		}
		mm := routeLabelRe.FindStringSubmatch(line)
		if mm == nil {
			continue
		}
		route := mm[1]
		routes = append(routes, route)
		if !knownRouteTemplates[route] {
			t.Fatalf("route label %q is NOT in the bounded template whitelist (cardinality leak)\nline: %s", route, line)
		}
		// belt-and-braces: the concrete job id must never appear in a route label.
		if strings.Contains(route, created.ID) {
			t.Fatalf("route label leaks the job id: %q", route)
		}
	}
	if len(routes) == 0 {
		t.Fatalf("no route labels found in scrape:\n%s", body)
	}
}

// TestMetricsTokenEnforced verifies that a configured metrics.token gates the
// endpoint: no/invalid token => 401, correct token => 200.
func TestMetricsTokenEnforced(t *testing.T) {
	const mt = "scrape-secret"
	s, _ := newMetricsServer(t, true, mt)

	if resp, _ := scrape(t, s, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-token scrape status=%d, want 401", resp.StatusCode)
	}
	if resp, _ := scrape(t, s, "wrong"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-token scrape status=%d, want 401", resp.StatusCode)
	}
	if resp, _ := scrape(t, s, mt); resp.StatusCode != http.StatusOK {
		t.Fatalf("correct-token scrape status=%d, want 200", resp.StatusCode)
	}
}

// TestMetricsDisabled asserts enabled=false drops the /metrics route entirely
// (the web SPA fallback / 404 handles it, not a metrics body).
func TestMetricsDisabled(t *testing.T) {
	s, _ := newMetricsServer(t, false, "")
	resp, body := scrape(t, s, "")
	// With no web console and metrics disabled, /metrics is an unmatched route =>
	// rux NotFound (404). It must NOT serve the prometheus exposition.
	if resp.StatusCode == http.StatusOK && strings.Contains(body, "go_goroutines") {
		t.Fatalf("metrics disabled but /metrics still served a scrape body")
	}
}

// metricValue parses the float value of the sample line whose series prefix
// exactly matches `series` (full `name{labels}` up to the space). It fails the
// test when the series is absent so callers get a clear miss.
func metricValue(t *testing.T, body, series string) float64 {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		sp := strings.LastIndexByte(line, ' ')
		if sp <= 0 {
			continue
		}
		if line[:sp] == series {
			v, err := strconv.ParseFloat(strings.TrimSpace(line[sp+1:]), 64)
			if err != nil {
				t.Fatalf("parse value for %q: %v (line=%q)", series, err, line)
			}
			return v
		}
	}
	return -1 // absent: caller's threshold check fails with context
}

// sampleLines returns all scrape lines whose name starts with prefix (for failure
// diagnostics).
func sampleLines(body, prefix string) string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, prefix) {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
