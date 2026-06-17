package httpapi

import (
	"io"
	"net/http"
	"testing"

	"dev-agent-bridge/internal/agent"
	"dev-agent-bridge/internal/config"
	"dev-agent-bridge/internal/job"
	"dev-agent-bridge/internal/project"
	"dev-agent-bridge/internal/runner"
	localrunner "dev-agent-bridge/internal/runner/local"
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
	jobs := job.NewService(cfg, projects, agents, runners)
	return New(&cfg.Server, testToken, false, jobs, projects, agents)
}

// TestWebConsoleMountedByDefault verifies the default server (WebEnabled nil =>
// true via newTestServer) serves the SPA shell for "/" and unknown front-end
// routes, while API and health routes still match first.
func TestWebConsoleMountedByDefault(t *testing.T) {
	s := newTestServer(t, testToken, false)

	// "/" -> placeholder SPA shell (200).
	resp := do(t, s, http.MethodGet, "/", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status=%d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(body) == 0 {
		t.Fatal("GET / returned empty body, want placeholder HTML")
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
