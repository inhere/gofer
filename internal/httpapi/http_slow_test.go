package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gookit/rux/v2"
)

// withSlowThreshold lowers slowRequestThreshold for one test and restores it.
func withSlowThreshold(t *testing.T, d time.Duration) {
	t.Helper()
	prev := slowRequestThreshold
	slowRequestThreshold = d
	t.Cleanup(func() { slowRequestThreshold = prev })
}

// captureSlowEvents collects the server.http_slow rows emitted by fn.
func captureSlowEvents(t *testing.T, fn func()) []map[string]any {
	t.Helper()
	var b bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&b, nil)))
	defer slog.SetDefault(old)
	fn()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(b.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal(line, &row); err == nil && row["event"] == "server.http_slow" {
			out = append(out, row)
		}
	}
	return out
}

// TestHTTPSlowMiddlewareOnRealRouter drives the middleware through the real router
// chain (buildRouter → Server.Handler): a request over the threshold produces one
// server.http_slow row naming the route TEMPLATE, the status, the elapsed time and
// the caller.
func TestHTTPSlowMiddlewareOnRealRouter(t *testing.T) {
	// 0ns: every request counts as slow (the clock's own granularity may make an
	// empty handler measure 0), so the test needs no sleep.
	withSlowThreshold(t, 0)
	s := newTestServer(t, testToken, false)
	events := captureSlowEvents(t, func() {
		r := httptest.NewRequest(http.MethodGet, "/v1/jobs?project=self&q=secret-query", nil)
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})
	if len(events) != 1 {
		t.Fatalf("slow events=%d, want 1: %#v", len(events), events)
	}
	e := events[0]
	if e["method"] != http.MethodGet || e["route"] != "/v1/jobs" || e["status"] != float64(http.StatusOK) {
		t.Fatalf("row=%v", e)
	}
	if _, ok := e["elapsed_ms"]; !ok {
		t.Fatalf("elapsed_ms missing: %v", e)
	}
	if _, ok := e["caller"]; !ok {
		t.Fatalf("caller missing: %v", e)
	}
	for _, bad := range []string{"secret-query", "q="} {
		if raw, _ := json.Marshal(e); bytes.Contains(raw, []byte(bad)) {
			t.Fatalf("query string leaked into the slow row: %s", raw)
		}
	}
}

// TestHTTPSlowMiddlewareSkipsStreamingRoutes: the SSE / WS / long-poll templates
// never produce a slow row, however long they stay open.
func TestHTTPSlowMiddlewareSkipsStreamingRoutes(t *testing.T) {
	// 0ns counts every request as slow, including one that measures 0
	// (the clock granularity is coarser than an empty handler).
	withSlowThreshold(t, 0)
	s := newTestServer(t, "", true)
	r := rux.New()
	r.Use(s.httpSlowMiddleware)
	hold := func(c *rux.Context) {}
	for route := range neverSlowRoutes {
		r.GET(route, hold)
	}
	r.GET("/fast", hold)
	events := captureSlowEvents(t, func() {
		for route := range neverSlowRoutes {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, route, nil))
		}
	})
	if len(events) != 0 {
		t.Fatalf("streaming routes logged as slow: %#v", events)
	}
	// The same threshold DOES fire for a normal route, so the test above proves the
	// exclusion and not a broken measurement.
	events = captureSlowEvents(t, func() {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/fast", nil))
	})
	if len(events) != 1 || events[0]["route"] != "/fast" {
		t.Fatalf("events=%#v", events)
	}
}

// TestHTTPSlowMiddlewareIgnoresFastRequests: under the threshold nothing is logged
// (the default 2s window is the production behaviour).
func TestHTTPSlowMiddlewareIgnoresFastRequests(t *testing.T) {
	s := newTestServer(t, "", true)
	r := rux.New()
	r.Use(s.httpSlowMiddleware)
	r.GET("/fast", func(c *rux.Context) { c.Resp.WriteHeader(http.StatusOK) })
	events := captureSlowEvents(t, func() {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/fast", nil))
	})
	if len(events) != 0 {
		t.Fatalf("fast request logged as slow: %#v", events)
	}
}
