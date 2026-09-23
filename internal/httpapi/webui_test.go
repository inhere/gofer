package httpapi

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

func boolptr(b bool) *bool { return &b }

// newWebServer builds a Server with an explicit web_enabled value, sharing the
// same single-project "self" wiring as newTestServer.
func newWebServer(t *testing.T, webEnabled bool) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken, WebEnabled: boolptr(webEnabled)},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	jobsEng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(jobsEng)
	return New(&cfg.Server, testToken, false, jobs, jobsEng, projects, agents, nil, nil, nil, nil)
}

// TestWebConsoleMountedByDefault verifies the default server (WebEnabled nil =>
// true via newTestServer) serves the web console shell for "/" and unknown
// front-end routes, while API and health routes still match first. It asserts
// only structural behaviour (HTML shell at "/", SPA fallback, auth unchanged),
// not the body text, so it stays green whether the embedded dist/ holds the
// placeholder (bare build) or a real `make web` build.
func TestWebConsoleMountedByDefault(t *testing.T) {
	s := newTestServer(t, testToken, false)

	// "/" -> web console shell (200, HTML).
	resp := do(t, s, http.MethodGet, "/", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status=%d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("GET / Content-Type=%q, want text/html", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(body) == 0 {
		t.Fatal("GET / returned empty body, want HTML shell")
	}

	// Unknown front-end route -> SPA fallback (200).
	resp = do(t, s, http.MethodGet, "/board", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /board status=%d, want 200 (SPA fallback)", resp.StatusCode)
	}
	resp.Body.Close()

	// /health still matches its concrete route.
	resp = do(t, s, http.MethodGet, "/health", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /health status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// /v1 API still requires the bearer token and works with it.
	resp = do(t, s, http.MethodGet, "/v1/projects", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/projects (auth) status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// /v1 without token still 401 (SPA fallback must not weaken auth).
	resp = do(t, s, http.MethodGet, "/v1/projects", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /v1/projects (no token) status=%d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestWebConsoleDisabled verifies that with web_enabled=false the SPA is not
// mounted: "/" 404s while the API keeps working.
func TestWebConsoleDisabled(t *testing.T) {
	s := newWebServer(t, false)

	resp := do(t, s, http.MethodGet, "/", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET / status=%d, want 404 (web disabled)", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, s, http.MethodGet, "/v1/projects", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/projects status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

// newWebDirServer builds the same wiring as newWebServer but serves the console
// from an on-disk dist directory (dev `--web-dir` mode), so a test can pin exact
// asset names instead of whatever the embedded build last produced.
func newWebDirServer(t *testing.T, webDir string) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken, WebEnabled: boolptr(true), WebDir: webDir},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	jobsEng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(jobsEng)
	return New(&cfg.Server, testToken, false, jobs, jobsEng, projects, agents, nil, nil, nil, nil)
}

// TestHeadServesShellAndAssets (S1, 2026-09-23): HEAD rides the SAME SPA handler
// as GET. A reverse proxy / health probe / CDN issues HEAD to check the route
// exists and to read its cache headers; a 404 on a path GET serves is a false
// "the console is down". The three outcomes are the contract — the shell is
// 200 + no-cache, a real asset 200 + immutable, a missing asset 404 (never the
// shell) — and no body is written for any of them.
func TestHeadServesShellAndAssets(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(`<!doctype html><div id="app"></div>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app-abc123.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newWebDirServer(t, dir)

	// HEAD / -> the shell: the browser must revalidate it (a cached shell keeps
	// naming the previous build's chunks).
	resp := do(t, s, http.MethodHead, "/", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD / status=%d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("HEAD / Cache-Control=%q, want no-cache", got)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(body) != 0 {
		t.Fatalf("HEAD / body=%q, want empty", body)
	}

	// HEAD of a real hashed asset: 200 and the immutable header GET sets.
	resp = do(t, s, http.MethodHead, "/assets/app-abc123.js", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD asset status=%d, want 200", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Cache-Control"), "public, max-age=31536000, immutable"; got != want {
		t.Fatalf("HEAD asset Cache-Control=%q, want %q", got, want)
	}
	resp.Body.Close()

	// A missing asset is a 404 for HEAD too: answering with the shell would make
	// the probe believe a stale chunk name still resolves.
	resp = do(t, s, http.MethodHead, "/assets/nope-DEADBEEF.js", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("HEAD missing asset status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// POST is still not the console's: 404 (the SPA is a read-only surface).
	resp = do(t, s, http.MethodPost, "/", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST / status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}
