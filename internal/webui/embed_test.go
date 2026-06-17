package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandlerPlaceholderWhenNoBuild verifies that with only the placeholder
// present (no dist/index.html in the current repo), Handler reports ok=false
// and serves the placeholder page (200) for GET requests.
func TestHandlerPlaceholderWhenNoBuild(t *testing.T) {
	h, ok := Handler()
	if ok {
		t.Fatal("expected ok=false (no real build, only placeholder)")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	resp := rec.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Web console not built") {
		t.Fatalf("placeholder body missing expected text, got: %q", body)
	}
}
