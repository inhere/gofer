package httpapi

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/project"
)

// builtinDefaultServer is a server whose config declares no `default` project, so the
// built-in one is injected (config.InjectDefaultProject).
func builtinDefaultServer(t *testing.T) (*Server, string) {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "ws")
	t.Setenv(config.EnvWorkspace, ws)
	cfg := &config.Config{Server: config.ServerConfig{Token: testToken}}
	s := newProjectWriteTestServerWith(t, cfg, func(c *config.Config) { config.InjectDefaultProject(c) })
	return s, ws
}

func TestBuiltinDefaultProjectIsListedAsInjected(t *testing.T) {
	s, ws := builtinDefaultServer(t)

	var list struct {
		Projects []string `json:"projects"`
		Injected []string `json:"injected"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/projects", testToken, nil), &list)
	if len(list.Injected) != 1 || list.Injected[0] != "default" {
		t.Fatalf("injected = %v, want [default]", list.Injected)
	}
	var one projectView
	decode(t, do(t, s, http.MethodGet, "/v1/projects/default", testToken, nil), &one)
	if !one.Injected || one.HostPath != ws {
		t.Fatalf("default view = %+v", one)
	}
	var cv configView
	decode(t, do(t, s, http.MethodGet, "/v1/config", testToken, nil), &cv)
	found := false
	for _, p := range cv.Projects {
		if p.Key == "default" {
			found = p.Injected
		}
	}
	if !found {
		t.Fatal("/v1/config must mark default injected")
	}
	var meta struct {
		Projects []struct {
			Key      string `json:"key"`
			Injected bool   `json:"injected"`
		} `json:"projects"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/meta", testToken, nil), &meta)
	if len(meta.Projects) == 0 || meta.Projects[0].Key != "default" || !meta.Projects[0].Injected {
		t.Fatalf("meta projects = %+v", meta.Projects)
	}
}

func TestBuiltinDefaultProjectCannotBeDeleted(t *testing.T) {
	s, _ := builtinDefaultServer(t)
	resp := do(t, s, http.MethodDelete, "/v1/projects/default", testToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete status = %d, want 409", resp.StatusCode)
	}
	if _, err := s.projects.Get("default"); err != nil {
		t.Fatalf("default vanished after a refused delete: %v", err)
	}
}

func TestEditingBuiltinDefaultWritesItIntoTheConfig(t *testing.T) {
	s, ws := builtinDefaultServer(t)
	resp := do(t, s, http.MethodPut, "/v1/projects/default", testToken, projectWriteReq{AllowExec: boolptr(true)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d", resp.StatusCode)
	}
	var one projectView
	decode(t, do(t, s, http.MethodGet, "/v1/projects/default", testToken, nil), &one)
	if one.Injected || !one.AllowExec {
		t.Fatalf("after edit: %+v (must be a declared project now)", one)
	}
	saved, _, err := config.Load(s.projects.Path())
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := saved.Projects["default"]
	if !ok || persisted.HostPath != ws || !persisted.AllowExec {
		t.Fatalf("saved default project = %+v, found=%t; want host_path %q and allow_exec true", persisted, ok, ws)
	}
	// Declared now, so it may be deleted like any project.
	if resp := do(t, s, http.MethodDelete, "/v1/projects/default", testToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete of the declared default status = %d", resp.StatusCode)
	}
}

func TestWorkOneShotProjectChooser(t *testing.T) {
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{
		"open":      {HostPath: "/open"},
		"claudeish": {HostPath: "/c", AllowedAgents: []string{"claude"}},
		"remote":    {HostPath: "/r", AllowedRunners: []string{"w1"}},
	}}
	o := workOneShot{projects: project.NewRegistry(cfg, "")}
	for _, tc := range []struct {
		key, agent string
		want       bool
	}{
		{"open", "claude", true}, {"claudeish", "claude", true}, {"claudeish", "codex", false},
		{"remote", "claude", false}, {"missing", "claude", false},
	} {
		if got := o.ProjectUsable(tc.key, tc.agent); got != tc.want {
			t.Errorf("ProjectUsable(%q,%q) = %v, want %v", tc.key, tc.agent, got, tc.want)
		}
	}
	if got := o.ProjectDir("open"); got != "/open" {
		t.Errorf("ProjectDir = %q", got)
	}
	if got := (workOneShot{}).ProjectDir("x"); got != "" {
		t.Errorf("no registry ProjectDir = %q", got)
	}
}
