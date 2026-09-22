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

// newTestSPAFS is a minimal built SPA: the shell plus one hashed asset, the
// shape vite emits (`assets/<Name>-<hash>.js`).
func newTestSPAFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte(`<!doctype html><div id="app"></div>`)},
		"assets/app-abc123.js": {Data: []byte("console.log('app')")},
	}
}

// TestMissingAssetIsNotTheShell (F8): a missing asset must 404. Answering with
// the shell (200 + HTML) is what killed the console after an upgrade — the
// browser got HTML where it asked for a module, the dynamic import rejected,
// and the router navigation died with only a MIME error to show for it.
func TestMissingAssetIsNotTheShell(t *testing.T) {
	h, ok := handlerFor(newTestSPAFS())
	if !ok {
		t.Fatal("expected ok=true (index.html present)")
	}

	// /assets/... is never a route (even without an extension); any other path
	// carrying a file extension is a missing file, not a front-end route.
	for _, p := range []string{"/assets/nope-DEADBEEF.js", "/assets/nameless-chunk", "/favicon.ico"} {
		resp := get(t, h, p)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s status=%d, want 404 (body: %q)", p, resp.StatusCode, body)
		}
		if strings.Contains(string(body), `id="app"`) || strings.Contains(strings.ToLower(string(body)), "<html") {
			t.Fatalf("GET %s was answered with the SPA shell, want a 404: %q", p, body)
		}
	}
}

// TestUnknownRouteFallsBackToShell (F8): extension-less unknown paths are
// client-side routes and still get the shell, so deep links keep working.
func TestUnknownRouteFallsBackToShell(t *testing.T) {
	h, ok := handlerFor(newTestSPAFS())
	if !ok {
		t.Fatal("expected ok=true (index.html present)")
	}

	for _, p := range []string{"/jobs/123", "/plans/plan-1/todos"} {
		resp := get(t, h, p)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status=%d, want 200 (SPA fallback)", p, resp.StatusCode)
		}
		if !strings.Contains(string(body), `id="app"`) {
			t.Fatalf("GET %s did not fall back to index.html, got: %q", p, body)
		}
	}
}

// TestShellIsNoCacheAssetsAreImmutable (F8): a cached shell keeps naming the
// previous build's chunks, so it must revalidate; hashed assets never change
// content under the same name and are immutable.
func TestShellIsNoCacheAssetsAreImmutable(t *testing.T) {
	h, _ := handlerFor(newTestSPAFS())

	// Both the shell itself and a route fallback into it are no-cache.
	for _, p := range []string{"/", "/jobs/123"} {
		resp := get(t, h, p)
		resp.Body.Close()
		if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("GET %s Cache-Control=%q, want %q", p, got, "no-cache")
		}
	}

	resp := get(t, h, "/assets/app-abc123.js")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /assets/app-abc123.js status=%d, want 200", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Cache-Control"), "public, max-age=31536000, immutable"; got != want {
		t.Fatalf("GET /assets/app-abc123.js Cache-Control=%q, want %q", got, want)
	}
}
