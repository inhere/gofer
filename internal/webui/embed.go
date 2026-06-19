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

// spaHandler serves files from fsys; requests whose path has no matching file
// are rewritten to "/" so the SPA shell (index.html) is returned, letting the
// client-side router handle the route.
func spaHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path
		if name == "" || name == "/" {
			fileServer.ServeHTTP(w, r)
			return
		}
		if _, err := fs.Stat(fsys, name[1:]); err != nil {
			// Unknown path: fall back to the SPA shell.
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
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
