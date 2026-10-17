package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// An unmatched path under /v1/ is an API miss: 404 with the JSON error body, never the
// SPA shell. Web routes outside /v1 keep the SPA fallback.
func TestUnknownV1PathIsJSON404(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/no-such-route-xyz"},
		{http.MethodPost, "/v1/no-such-route-xyz"},
		{http.MethodGet, "/v1/agents/presence"}, // removed route
		{http.MethodGet, "/v1/deep/unknown/path"},
		{http.MethodHead, "/v1/no-such-route-xyz"},
	} {
		for _, tok := range []string{testToken, ""} {
			resp := do(t, s, tc.method, tc.path, tok, nil)
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("%s %s (token=%q) status=%d, want 404", tc.method, tc.path, tok, resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
				t.Fatalf("%s %s Content-Type=%q, want application/json", tc.method, tc.path, ct)
			}
			if tc.method == http.MethodHead {
				continue
			}
			var eb struct{ Error, Detail string }
			if err := json.Unmarshal(body, &eb); err != nil || eb.Error == "" || eb.Detail == "" {
				t.Fatalf("%s %s body=%q, want JSON error/detail (err=%v)", tc.method, tc.path, body, err)
			}
			if strings.Contains(strings.ToLower(string(body)), "<html") {
				t.Fatalf("%s %s returned the SPA shell", tc.method, tc.path)
			}
		}
	}

	// Front-end routes outside /v1 still get the SPA shell.
	for _, p := range []string{"/work", "/board"} {
		resp := do(t, s, http.MethodGet, p, "", nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("GET %s status=%d ct=%q, want 200 text/html", p, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
}

// With the web console disabled an unknown /v1 path is still the JSON 404.
func TestUnknownV1PathIsJSON404WebDisabled(t *testing.T) {
	t.Parallel()
	s := newWebServer(t, false)
	resp := do(t, s, http.MethodGet, "/v1/no-such-route-xyz", testToken, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("status=%d ct=%q body=%q, want JSON 404", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
}
