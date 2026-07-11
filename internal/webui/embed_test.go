package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// get drives handlerFor's returned http.Handler against path and returns the
// recorded response.
func get(t *testing.T, h http.Handler, path string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

// TestHandlerForPlaceholder verifies that with only placeholder.html present
// (no index.html — i.e. a bare build that never ran `make web`), handlerFor
// reports ok=false and serves the placeholder page (200) for GET requests.
// It uses an in-memory FS so the result is independent of the real embedded
// dist/ contents.
func TestHandlerForPlaceholder(t *testing.T) {
	fsys := fstest.MapFS{
		"placeholder.html": {Data: []byte("<h1>Web console not built</h1>")},
	}

	h, ok := handlerFor(fsys)
	if ok {
		t.Fatal("expected ok=false (only placeholder, no index.html)")
	}

	resp := get(t, h, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status=%d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Web console not built") {
		t.Fatalf("placeholder body missing expected text, got: %q", body)
	}
}

// TestHandlerForSPA verifies that with index.html + an asset present (a real
// build), handlerFor reports ok=true and: serves index.html for "/", falls back
// to index.html for unknown front-end routes, and serves real asset files when
// they exist.
func TestHandlerForSPA(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":    {Data: []byte(`<!doctype html><div id="app"></div>`)},
		"assets/app.js": {Data: []byte("console.log('app')")},
	}

	h, ok := handlerFor(fsys)
	if !ok {
		t.Fatal("expected ok=true (index.html present)")
	}

	// "/" serves the SPA shell.
	resp := get(t, h, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status=%d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `id="app"`) {
		t.Fatalf("GET / body missing SPA shell, got: %q", body)
	}

	// Unknown front-end route falls back to index.html.
	resp = get(t, h, "/board")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /board status=%d, want 200 (SPA fallback)", resp.StatusCode)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `id="app"`) {
		t.Fatalf("GET /board did not fall back to index.html, got: %q", body)
	}

	// Real asset is served directly.
	resp = get(t, h, "/assets/app.js")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /assets/app.js status=%d, want 200", resp.StatusCode)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "console.log") {
		t.Fatalf("GET /assets/app.js missing asset content, got: %q", body)
	}
}

func TestHandlerForDirSPA(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(`<!doctype html><div id="disk-app"></div>`), 0o600); err != nil {
		t.Fatalf("write index.html: %v", err)
	}

	h, ok := HandlerForDir(dir)
	if !ok {
		t.Fatal("expected ok=true (index.html present)")
	}

	resp := get(t, h, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status=%d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `id="disk-app"`) {
		t.Fatalf("GET / body missing disk SPA shell, got: %q", body)
	}
}

func TestHandlerForDirPlaceholder(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "placeholder.html"), []byte("<h1>Placeholder from disk</h1>"), 0o600); err != nil {
		t.Fatalf("write placeholder.html: %v", err)
	}

	h, ok := HandlerForDir(dir)
	if ok {
		t.Fatal("expected ok=false (no index.html)")
	}

	resp := get(t, h, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status=%d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Placeholder from disk") {
		t.Fatalf("placeholder body missing expected text, got: %q", body)
	}
}
