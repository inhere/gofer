package httpapi

import (
	"net/http"
	"path/filepath"
	"slices"
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

// projectWriteReq's value fields are pointers ("absent = keep the stored value"), so
// the tests below build requests through these shorthands. boolptr lives in
// webui_test.go (same package).
func strPtr(s string) *string       { return &s }
func strsPtr(s ...string) *[]string { return &s }
func intPtr(i int) *int             { return &i }

func TestCreateProjectWritesAndCanBeRead(t *testing.T) {
	root := t.TempDir()
	s := newProjectWriteTestServer(t, &config.Config{
		Server: config.ServerConfig{Token: testToken},
	})

	resp := do(t, s, http.MethodPost, "/v1/projects", testToken, projectWriteReq{
		Key:               "demo",
		HostPath:          strPtr(root),
		DefaultAgent:      strPtr("claude"),
		AllowedAgents:     strsPtr("claude"),
		AllowedRunners:    strsPtr("local", "peer"),
		AllowExec:         boolptr(true),
		MaxConcurrentJobs: intPtr(2),
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

	resp := do(t, s, http.MethodPost, "/v1/projects", testToken, projectWriteReq{Key: "demo", HostPath: strPtr(root)})
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
		HostPath:      strPtr(root),
		AllowedAgents: strsPtr("ghost"),
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid agent status=%d, want 400", resp.StatusCode)
	}
	if _, err := s.projects.Get("bad-agent"); err == nil {
		t.Fatal("bad-agent was written after invalid reference")
	}

	resp = do(t, s, http.MethodPost, "/v1/projects", testToken, projectWriteReq{
		Key:            "bad-runner",
		HostPath:       strPtr(root),
		AllowedRunners: strsPtr("ghost-runner"),
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
		HostPath:       strPtr(root),
		DefaultAgent:   strPtr("exec"),
		AllowedAgents:  strsPtr("exec"),
		AllowedRunners: strsPtr("peer"),
		AllowExec:      boolptr(true),
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

	createReq := projectWriteReq{Key: "new", HostPath: strPtr(root)}
	if resp := do(t, s, http.MethodPost, "/v1/projects", "reader-token", createReq); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reader create status=%d, want 403", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPut, "/v1/projects/demo", "reader-token", projectWriteReq{HostPath: strPtr(root)}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reader update status=%d, want 403", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodDelete, "/v1/projects/demo", "reader-token", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reader delete status=%d, want 403", resp.StatusCode)
	}

	if resp := do(t, s, http.MethodPost, "/v1/projects", "admin-token", createReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin create status=%d, want 200", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPut, "/v1/projects/new", "admin-token", projectWriteReq{HostPath: strPtr(root), AllowExec: boolptr(true)}); resp.StatusCode != http.StatusOK {
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

	resp := do(t, s, http.MethodPost, "/v1/projects", testToken, projectWriteReq{Key: "warn", HostPath: strPtr(missing)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created projectWriteResp
	decode(t, resp, &created)
	if len(created.Warnings) == 0 {
		t.Fatalf("warnings empty, want missing path warning")
	}
}

// TestUpdateProjectPreservesUnspecifiedFields (bd h-aii-3scy): a PUT carrying only
// part of the form must leave every field it does not carry exactly as stored. The
// pre-fix handler rebuilt the whole ProjectConfig from the form, so a console save
// silently wiped everything the form does not know about — exchange_subdir /
// result_subdir here, capture_diff / notify_enabled likewise, and the admission fields
// the request omits.
func TestUpdateProjectPreservesUnspecifiedFields(t *testing.T) {
	root := t.TempDir()
	captureOff := false
	cfg := &config.Config{
		Server: config.ServerConfig{Token: testToken},
		Agents: map[string]config.AgentConfig{
			"tty-echo": {Type: agent.TypeCLIAgent, Command: "echo", InteractiveArgs: []string{}},
		},
		Projects: map[string]config.ProjectConfig{
			"demo": {
				HostPath:                 root,
				ExchangeSubdir:           "tmp",
				ResultSubdir:             "gofer",
				DefaultAgent:             "claude",
				AllowedAgents:            []string{"claude", "tty-echo"},
				AllowedRunners:           []string{"local", "peer"},
				InteractiveAllowedAgents: []string{"tty-echo"},
				CaptureDiff:              &captureOff,
			},
		},
	}
	s := newProjectWriteTestServer(t, cfg)

	resp := do(t, s, http.MethodPut, "/v1/projects/demo", testToken, projectWriteReq{AllowExec: boolptr(true)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status=%d, want 200", resp.StatusCode)
	}

	stored, err := s.projects.Get("demo")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if stored.ExchangeSubdir != "tmp" || stored.ResultSubdir != "gofer" {
		t.Fatalf("subdirs dropped by a partial PUT: exchange=%q result=%q", stored.ExchangeSubdir, stored.ResultSubdir)
	}
	if stored.HostPath != root || stored.DefaultAgent != "claude" {
		t.Fatalf("path/default_agent changed by a partial PUT: %+v", stored)
	}
	if !slices.Equal(stored.AllowedAgents, []string{"claude", "tty-echo"}) ||
		!slices.Equal(stored.AllowedRunners, []string{"local", "peer"}) {
		t.Fatalf("allowlists dropped by a partial PUT: %+v", stored)
	}
	if !slices.Equal(stored.InteractiveAllowedAgents, []string{"tty-echo"}) || !stored.IsInteractiveAllowed() {
		t.Fatalf("interactive narrowing dropped by a partial PUT: %+v", stored)
	}
	if stored.CaptureDiff == nil || *stored.CaptureDiff {
		t.Fatalf("capture_diff dropped by a partial PUT: %v", stored.CaptureDiff)
	}
	if !stored.AllowExec {
		t.Fatal("the field the request DID carry (allow_exec) was not applied")
	}
}

// TestUpdateProjectInteractiveSettingsRoundTrip: the AGT-02 switch and the narrowing
// list survive create → read → update. The switch is read back RESOLVED (the console
// never has to know the legacy rule), and an explicit false written by the switch is
// not resurrected by the leftover list.
func TestUpdateProjectInteractiveSettingsRoundTrip(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		Server: config.ServerConfig{Token: testToken},
		Agents: map[string]config.AgentConfig{
			"tty-echo": {Type: agent.TypeCLIAgent, Command: "echo", InteractiveArgs: []string{}},
		},
	}
	s := newProjectWriteTestServer(t, cfg)

	resp := do(t, s, http.MethodPost, "/v1/projects", testToken, projectWriteReq{
		Key:                      "demo",
		HostPath:                 strPtr(root),
		AllowedAgents:            strsPtr("tty-echo"),
		AllowInteractive:         boolptr(true),
		InteractiveAllowedAgents: strsPtr("tty-echo"),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created projectWriteResp
	decode(t, resp, &created)
	if !created.AllowInteractive || !slices.Equal(created.InteractiveAllowedAgents, []string{"tty-echo"}) {
		t.Fatalf("created project = %+v, want the interactive switch + narrowing echoed back", created)
	}

	resp = do(t, s, http.MethodGet, "/v1/projects/demo", testToken, nil)
	var got projectView
	decode(t, resp, &got)
	if !got.AllowInteractive || !slices.Equal(got.InteractiveAllowedAgents, []string{"tty-echo"}) {
		t.Fatalf("GET project = %+v, want allow_interactive with the narrowing list", got)
	}

	// Flip the switch off, leaving the narrowing list in place: the stored pointer must
	// be an explicit false (not "unset", which the legacy list would read as true).
	resp = do(t, s, http.MethodPut, "/v1/projects/demo", testToken, projectWriteReq{AllowInteractive: boolptr(false)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status=%d, want 200", resp.StatusCode)
	}
	resp = do(t, s, http.MethodGet, "/v1/projects/demo", testToken, nil)
	got = projectView{}
	decode(t, resp, &got)
	if got.AllowInteractive {
		t.Fatalf("GET project = %+v, want allow_interactive false after the flip", got)
	}
	if !slices.Equal(got.InteractiveAllowedAgents, []string{"tty-echo"}) {
		t.Fatalf("the narrowing list must survive a switch-only PUT, got %v", got.InteractiveAllowedAgents)
	}
	stored, err := s.projects.Get("demo")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.AllowInteractive == nil || *stored.AllowInteractive {
		t.Fatalf("stored AllowInteractive = %v, want an explicit false", stored.AllowInteractive)
	}
	// And the WRITTEN yaml must carry it: omitempty on a *bool drops only nil, so a
	// reload still reads an explicit false. If the write lost it, the next serve start
	// would read "unset" and the leftover narrowing list would flip this project back
	// to allowed — the exact reversal the switch exists to prevent.
	persisted, _, err := config.Load(s.projects.Path())
	if err != nil {
		t.Fatalf("reload persisted config: %v", err)
	}
	reloaded := persisted.Projects["demo"]
	if reloaded.AllowInteractive == nil || *reloaded.AllowInteractive || reloaded.IsInteractiveAllowed() {
		t.Fatalf("persisted project = %+v, want an explicit allow_interactive:false", reloaded)
	}
}

// TestUpdateProjectRejectsNonInteractiveNarrowing: every entry of the narrowing list
// must name a defined agent that CAN run interactively and, when allowed_agents is
// non-empty, one inside that whitelist — otherwise the console offers a submit that
// admission is guaranteed to reject. The error names the offending agent.
func TestUpdateProjectRejectsNonInteractiveNarrowing(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		Server: config.ServerConfig{Token: testToken},
		Agents: map[string]config.AgentConfig{
			"tty-echo": {Type: agent.TypeCLIAgent, Command: "echo", InteractiveArgs: []string{}},
		},
		Projects: map[string]config.ProjectConfig{
			"demo": {HostPath: root, AllowedAgents: []string{"claude"}, AllowInteractive: boolptr(true)},
		},
	}
	s := newProjectWriteTestServer(t, cfg)

	cases := []struct {
		name    string
		narrow  []string
		wantMsg string
	}{
		{"unnamed agent", []string{"ghost"}, `agent "ghost" is not defined`},
		{"agent without an interactive mode", []string{"claude"}, `agent "claude" has no interactive mode`},
		{"agent outside allowed_agents", []string{"tty-echo"}, `agent "tty-echo" is not in allowed_agents`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(t, s, http.MethodPut, "/v1/projects/demo", testToken, projectWriteReq{
				InteractiveAllowedAgents: &tc.narrow,
			})
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("update status=%d, want 400", resp.StatusCode)
			}
			var body struct {
				Detail string `json:"detail"`
			}
			decode(t, resp, &body)
			if !strings.Contains(body.Detail, tc.wantMsg) {
				t.Fatalf("detail = %q, want it to contain %q", body.Detail, tc.wantMsg)
			}
			stored, err := s.projects.Get("demo")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if len(stored.InteractiveAllowedAgents) != 0 {
				t.Fatalf("a rejected update must not be stored, got %v", stored.InteractiveAllowedAgents)
			}
		})
	}
}
