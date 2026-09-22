// Package webui embeds the gofer web console static assets into the
// binary so `serve` can mount the SPA without any external files.
//
// A bare `go build` (without first running `make web`) still compiles because
// dist/ always contains placeholder.html — the //go:embed directive needs at
// least one file present. When a real build is present (dist/index.html),
// Handler serves the SPA with index.html fallback for unknown routes; otherwise
// it serves the placeholder page.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// dist holds the built web console assets. all: includes files whose names
// start with "." or "_" too, matching whatever a frontend build emits.
//
//go:embed all:dist
var dist embed.FS

// Handler returns the web console static handler and whether a real build is
// present (true) vs the build-time placeholder (false). When dist/index.html
// exists it serves the SPA (unknown routes fall back to index.html); otherwise
// it serves the placeholder page for any GET. Callers (the HTTP server) GET-gate
// requests before delegating here.
func Handler() (http.Handler, bool) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return placeholderHandler(dist, "dist/placeholder.html"), false
	}
	return handlerFor(sub)
}

// HandlerForDir serves the web console from an on-disk directory (dev convenience,
// P7): `gofer serve --web-dir web/dist` avoids re-embedding via `make web` on every
// front-end change. dir must be the built SPA root (containing index.html). Returns
// the handler and whether a real build is present (index.html exists).
func HandlerForDir(dir string) (http.Handler, bool) {
	return handlerFor(os.DirFS(dir))
}

// handlerFor builds the static handler for fsys (a "dist" sub-FS): when
// index.html exists it serves the SPA with unknown-route fallback (ok=true);
// otherwise it serves placeholder.html for any GET (ok=false). It is pure (no
// dependency on the package-level embed.FS) so tests can drive it with an
// in-memory fstest.MapFS — independent of whether `make web` has run.
func handlerFor(fsys fs.FS) (http.Handler, bool) {
	if _, err := fs.Stat(fsys, "index.html"); err == nil {
		return spaHandler(fsys), true
	}
	return placeholderHandler(fsys, "placeholder.html"), false
}

// spaHandler serves files from fsys. A path with no matching file is rewritten
// to "/" (serving the SPA shell) only when it looks like a client-side route —
// an extension-less path such as /jobs/123, which is how the front-end router
// addresses views. Anything else is a genuinely missing file and gets 404:
// falling back to index.html for /assets/Plans-DM0PtMfW.js would answer a
// script request with HTML, which the browser rejects as a module and the
// dynamic import rejects, leaving navigation dead with only a MIME error in the
// console. A 404 instead lets the client detect the stale-chunk case and reload.
//
// Cache headers: the shell must never be cached (it names the hashed assets, so
// a cached shell keeps pointing at the previous build's chunk names), while
// hashed asset names change with their content and are immutable.
func spaHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path
		if name == "" || name == "/" {
			serveShell(w, r, fileServer)
			return
		}
		if _, err := fs.Stat(fsys, name[1:]); err != nil {
			if !isSPARoute(name) {
				http.NotFound(w, r)
				return
			}
			serveShell(w, r, fileServer)
			return
		}
		if strings.HasPrefix(name, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// isSPARoute reports whether p looks like a front-end route rather than a file:
// extension-less (`path.Ext` == "") and not under /assets/ (a vite asset whose
// name lost its hash is still an asset, never a route).
func isSPARoute(p string) bool {
	if strings.HasPrefix(p, "/assets/") {
		return false
	}
	return path.Ext(p) == ""
}

// serveShell serves index.html for r (as "/") with no-cache, so the browser
// revalidates the shell on every load and picks up a new build's asset names.
func serveShell(w http.ResponseWriter, r *http.Request, fileServer http.Handler) {
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/"
	w.Header().Set("Cache-Control", "no-cache")
	fileServer.ServeHTTP(w, r2)
}

// placeholderHandler serves the embedded placeholder page (200) for any request;
// it is only reached after the caller has GET-gated the request.
func placeholderHandler(fsys fs.FS, name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			http.Error(w, "web console placeholder missing", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
}
