package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gookit/rux/v2"
	"github.com/inhere/gofer/internal/config"
)

func captureHTTPErrorEvents(t *testing.T, fn func()) []map[string]any {
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
		if err := json.Unmarshal(line, &row); err == nil && row["event"] == "server.http_error" {
			out = append(out, row)
		}
	}
	return out
}

func TestHTTPErrorMiddlewareStatuses(t *testing.T) {
	s := newTestServer(t, testToken, false)
	events := captureHTTPErrorEvents(t, func() {
		r := httptest.NewRequest(http.MethodGet, "/v1/projects?secret=redact", nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
	})
	if len(events) != 1 {
		t.Fatalf("http error events=%d, want 1: %#v", len(events), events)
	}
	for _, e := range events {
		if e["path"] == "/v1/projects?secret=redact" {
			t.Fatal("query string leaked into path")
		}
		if e["status"] != float64(http.StatusUnauthorized) {
			t.Fatalf("status=%v, want 401", e["status"])
		}
	}
}

func TestHTTPErrorMiddleware403(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{Token: testToken, Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "worker-token"}}})
	s.hub = &tunnelTestHub{live: true}
	s.router = s.buildRouter()
	events := captureHTTPErrorEvents(t, func() {
		r := httptest.NewRequest(http.MethodGet, "/v1/tunnels/connect?worker=w1&target=127.0.0.1:80", nil)
		r.Header.Set("Authorization", "Bearer worker-token")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status=%d, want 403", w.Code)
		}
	})
	if len(events) != 1 || events[0]["status"] != float64(http.StatusForbidden) {
		t.Fatalf("events=%#v", events)
	}
}

func TestHTTPErrorMiddleware500AndSuccess(t *testing.T) {
	s := newTestServer(t, "", true)
	r := rux.New()
	r.Use(s.httpErrorMiddleware)
	r.GET("/boom", func(c *rux.Context) { panic("boom-secret") })
	r.GET("/ok", func(c *rux.Context) { c.Resp.WriteHeader(http.StatusOK); _, _ = c.Resp.Write([]byte("ok")) })
	events := captureHTTPErrorEvents(t, func() {
		for _, path := range []string{"/boom", "/ok"} {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if path == "/boom" && w.Code != http.StatusInternalServerError {
				t.Fatalf("panic status=%d", w.Code)
			}
			if path == "/ok" && w.Code != http.StatusOK {
				t.Fatalf("ok status=%d", w.Code)
			}
		}
	})
	if len(events) != 1 || events[0]["status"] != float64(http.StatusInternalServerError) {
		t.Fatalf("events=%#v", events)
	}
}
