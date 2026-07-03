package httpapi

import (
	"net/http"
	"path/filepath"
	"slices"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

func newProjectWriteTestServer(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	root := t.TempDir()
	if cfg.Storage.Root == "" {
		cfg.Storage.Root = filepath.Join(root, "store")
	}
	if cfg.Projects == nil {
		cfg.Projects = map[string]config.ProjectConfig{}
	}
	if cfg.Agents == nil {
		cfg.Agents = map[string]config.AgentConfig{}
	}
	cfg.Agents["claude"] = config.AgentConfig{Type: "cli"}
	cfg.Agents["exec"] = config.AgentConfig{Type: "cli"}
	if cfg.Runners == nil {
		cfg.Runners = map[string]config.RunnerConfig{}
	}
	cfg.Runners["peer"] = config.RunnerConfig{Type: "peer-http", BaseURL: "http://peer.example.test"}

	registryPath := filepath.Join(root, "config.yaml")
	projects := project.NewRegistry(cfg, registryPath)
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil)
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return New(&cfg.Server, cfg.Server.Token, cfg.Server.AllowEmptyToken, jobs, eng, projects, agents, nil, cfg.Runners, nil, nil)
}

func TestCreateProjectWritesAndCanBeRead(t *testing.T) {
	root := t.TempDir()
	s := newProjectWriteTestServer(t, &config.Config{
		Server: config.ServerConfig{Token: testToken},
	})

	resp := do(t, s, http.MethodPost, "/v1/projects", testToken, projectWriteReq{
		Key:               "demo",
		HostPath:          root,
		DefaultAgent:      "claude",
		AllowedAgents:     []string{"claude"},
		AllowedRunners:    []string{"local", "peer"},
		AllowExec:         true,
		MaxConcurrentJobs: 2,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created projectWriteResp
	decode(t, resp, &created)
	if created.Key != "demo" || created.HostPath != root || !created.AllowExec || created.MaxConcurrentJobs != 2 {
		t.Fatalf("created project = %+v", created)
	}
	if len(created.Warnings) != 0 {
		t.Fatalf("warnings=%v, want none", created.Warnings)
	}

	resp = do(t, s, http.MethodGet, "/v1/projects/demo", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status=%d, want 200", resp.StatusCode)
	}
	var got projectView
	decode(t, resp, &got)
	if got.Key != "demo" || got.DefaultAgent != "claude" || !slices.Equal(got.AllowedRunners, []string{"local", "peer"}) {
		t.Fatalf("got project = %+v", got)
	}

	resp = do(t, s, http.MethodGet, "/v1/projects", testToken, nil)
	var listed struct {
		Projects []string `json:"projects"`
	}
	decode(t, resp, &listed)
	if !slices.Contains(listed.Projects, "demo") {
		t.Fatalf("projects=%v, want demo", listed.Projects)
	}
}

func TestCreateProjectDuplicateReturnsConflict(t *testing.T) {
	root := t.TempDir()
	s := newProjectWriteTestServer(t, &config.Config{
		Server: config.ServerConfig{Token: testToken},
		Projects: map[string]config.ProjectConfig{
			"demo": {HostPath: root},
		},
	})

	resp := do(t, s, http.MethodPost, "/v1/projects", testToken, projectWriteReq{Key: "demo", HostPath: root})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate status=%d, want 409", resp.StatusCode)
	}
}

func TestCreateProjectRejectsInvalidReferencesWithoutWriting(t *testing.T) {
	root := t.TempDir()
	s := newProjectWriteTestServer(t, &config.Config{
		Server: config.ServerConfig{Token: testToken},
	})

	resp := do(t, s, http.MethodPost, "/v1/projects", testToken, projectWriteReq{
		Key:           "bad-agent",
		HostPath:      root,
		AllowedAgents: []string{"ghost"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid agent status=%d, want 400", resp.StatusCode)
	}
	if _, err := s.projects.Get("bad-agent"); err == nil {
		t.Fatal("bad-agent was written after invalid reference")
	}

	resp = do(t, s, http.MethodPost, "/v1/projects", testToken, projectWriteReq{
		Key:            "bad-runner",
		HostPath:       root,
		AllowedRunners: []string{"ghost-runner"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid runner status=%d, want 400", resp.StatusCode)
	}
	if _, err := s.projects.Get("bad-runner"); err == nil {
		t.Fatal("bad-runner was written after invalid reference")
	}
}

func TestCreateProjectRejectsEmptyHostPath(t *testing.T) {
	s := newProjectWriteTestServer(t, &config.Config{
		Server: config.ServerConfig{Token: testToken},
	})

	resp := do(t, s, http.MethodPost, "/v1/projects", testToken, projectWriteReq{Key: "empty-host"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty host_path status=%d, want 400", resp.StatusCode)
	}
	if _, err := s.projects.Get("empty-host"); err == nil {
		t.Fatal("empty-host was written")
	}
}

func TestUpdateProjectOverwritesAdmission(t *testing.T) {
	root := t.TempDir()
	s := newProjectWriteTestServer(t, &config.Config{
		Server: config.ServerConfig{Token: testToken},
		Projects: map[string]config.ProjectConfig{
			"demo": {HostPath: root, AllowedAgents: []string{"claude"}, AllowedRunners: []string{"local"}},
		},
	})

	resp := do(t, s, http.MethodPut, "/v1/projects/demo", testToken, projectWriteReq{
		Key:            "ignored",
		HostPath:       root,
		DefaultAgent:   "exec",
		AllowedAgents:  []string{"exec"},
		AllowedRunners: []string{"peer"},
		AllowExec:      true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status=%d, want 200", resp.StatusCode)
	}

	resp = do(t, s, http.MethodGet, "/v1/projects/demo", testToken, nil)
	var got projectView
	decode(t, resp, &got)
	if !got.AllowExec || got.DefaultAgent != "exec" || !slices.Equal(got.AllowedAgents, []string{"exec"}) || !slices.Equal(got.AllowedRunners, []string{"peer"}) {
		t.Fatalf("updated project = %+v", got)
	}
}

func TestDeleteProjectRemovesMapping(t *testing.T) {
	root := t.TempDir()
	s := newProjectWriteTestServer(t, &config.Config{
		Server: config.ServerConfig{Token: testToken},
		Projects: map[string]config.ProjectConfig{
			"demo": {HostPath: root},
		},
	})

	resp := do(t, s, http.MethodDelete, "/v1/projects/demo", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status=%d, want 200", resp.StatusCode)
	}
	resp = do(t, s, http.MethodGet, "/v1/projects/demo", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete status=%d, want 404", resp.StatusCode)
	}
	resp = do(t, s, http.MethodGet, "/v1/projects", testToken, nil)
	var listed struct {
		Projects []string `json:"projects"`
	}
	decode(t, resp, &listed)
	if slices.Contains(listed.Projects, "demo") {
		t.Fatalf("projects=%v, did not want demo", listed.Projects)
	}

	resp = do(t, s, http.MethodDelete, "/v1/projects/demo", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete unknown status=%d, want 404", resp.StatusCode)
	}
}

func TestProjectWritesRequireAdminWhenGateEnabled(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Governance: config.GovernanceConfig{RequireAdminCapability: true},
			Callers: []config.CallerConfig{
				{ID: "reader", Token: "reader-token"},
				{ID: "admin", Token: "admin-token", CanAdmin: true},
			},
		},
		Projects: map[string]config.ProjectConfig{
			"demo": {HostPath: root},
		},
	}
	s := newProjectWriteTestServer(t, cfg)

	createReq := projectWriteReq{Key: "new", HostPath: root}
	if resp := do(t, s, http.MethodPost, "/v1/projects", "reader-token", createReq); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reader create status=%d, want 403", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPut, "/v1/projects/demo", "reader-token", projectWriteReq{HostPath: root}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reader update status=%d, want 403", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodDelete, "/v1/projects/demo", "reader-token", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reader delete status=%d, want 403", resp.StatusCode)
	}

	if resp := do(t, s, http.MethodPost, "/v1/projects", "admin-token", createReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin create status=%d, want 200", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPut, "/v1/projects/new", "admin-token", projectWriteReq{HostPath: root, AllowExec: true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin update status=%d, want 200", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodDelete, "/v1/projects/new", "admin-token", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin delete status=%d, want 200", resp.StatusCode)
	}
}

func TestCreateProjectReturnsFilesystemWarnings(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	s := newProjectWriteTestServer(t, &config.Config{
		Server: config.ServerConfig{Token: testToken},
	})

	resp := do(t, s, http.MethodPost, "/v1/projects", testToken, projectWriteReq{Key: "warn", HostPath: missing})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created projectWriteResp
	decode(t, resp, &created)
	if len(created.Warnings) == 0 {
		t.Fatalf("warnings empty, want missing path warning")
	}
}
