package commands

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tracker"
)

func mkGit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func projCfg(roots map[string]string) *config.Config {
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{}}
	for k, p := range roots {
		cfg.Projects[k] = config.ProjectConfig{HostPath: p}
	}
	return cfg
}

func TestTrackerProjectKeyNotes(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "ws")
	outer := filepath.Join(proj, "outer")
	inner := filepath.Join(outer, "tools", "inner")
	for _, d := range []string{proj, outer, inner} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mkGit(t, outer)
	mkGit(t, inner)
	cfg := projCfg(map[string]string{"ws": proj, "other": filepath.Join(base, "other")})

	// longest-prefix match is named with its basis
	key, _, _ := cfg.ProjectMatchForPath(outer)
	notes := strings.Join(trackerProjectKeyNotes(cfg, outer, key), "\n")
	if !strings.Contains(notes, "project_key: ws") || !strings.Contains(notes, "最长路径前缀 "+proj) || strings.Contains(notes, "嵌套") {
		t.Fatalf("outer notes: %s", notes)
	}

	// nested standalone repo under another repo inside the project: extra hint
	notes = strings.Join(trackerProjectKeyNotes(cfg, inner, "ws"), "\n")
	if !strings.Contains(notes, "嵌套的独立仓库") || !strings.Contains(notes, outer) || !strings.Contains(notes, "project_key") {
		t.Fatalf("nested notes: %s", notes)
	}

	// the project root itself is never "nested"
	mkGit(t, proj)
	if got := nestedOuterRepo(proj, proj); got != "" {
		t.Fatalf("project root flagged nested in %s", got)
	}
	// a repo directly in the project dir whose project root IS the outer repo
	if got := nestedOuterRepo(outer, proj); got != proj {
		t.Fatalf("outer under project repo: %q", got)
	}

	// unmatched: explains how to fill it
	notes = strings.Join(trackerProjectKeyNotes(cfg, filepath.Join(base, "nowhere"), ""), "\n")
	if !strings.Contains(notes, "未匹配到") || !strings.Contains(notes, "config.yaml") {
		t.Fatalf("unmatched notes: %s", notes)
	}
	if got := trackerProjectKeyNotes(nil, outer, ""); len(got) != 1 || !strings.Contains(got[0], "project_key") {
		t.Fatalf("no config: %v", got)
	}
}

func TestBindTrackerProjectKeyFromServer(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "work", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	// A client node: the local config declares no projects at all.
	cfgFile := filepath.Join(base, "config.yaml")
	if err := os.WriteFile(cfgFile, []byte("server:\n  addr: 127.0.0.1:1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := config.InputCfgFile
	config.InputCfgFile = cfgFile
	t.Cleanup(func() { config.InputCfgFile = old })

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"projects":[{"key":"hostview","host_path":"/nowhere/else"},` +
			`{"key":"appkey","host_path":"/host/app","container_path":"` + filepath.ToSlash(base) + `/work"},` +
			`{"key":"deeper","container_path":"` + filepath.ToSlash(repo) + `"}]}`))
	}))
	defer srv.Close()
	fetch := func() ([]client.ProjectMeta, error) { return client.New(srv.URL, "t").ListProjects() }

	s, _, err := tracker.Init(repo, "cl", true)
	if err != nil {
		t.Fatal(err)
	}
	notes := strings.Join(bindTrackerProjectKeyWith(s, repo, fetch), "\n")
	if !strings.Contains(notes, "project_key: deeper") || !strings.Contains(notes, "server") {
		t.Fatalf("notes: %s", notes)
	}
	if hits != 1 {
		t.Fatalf("server hits=%d", hits)
	}
	if cfg, err := s.ReadConfig(); err != nil || cfg.ProjectKey != "deeper" {
		t.Fatalf("project_key not written: %+v err=%v", cfg, err)
	}

	// Server answers but nothing matches: the hand-fill hint remains.
	other := filepath.Join(base, "unrelated")
	_ = os.MkdirAll(other, 0o755)
	s2, _, err := tracker.Init(other, "cl", true)
	if err != nil {
		t.Fatal(err)
	}
	notes = strings.Join(bindTrackerProjectKeyWith(s2, other, fetch), "\n")
	if !strings.Contains(notes, "未匹配到") {
		t.Fatalf("unmatched notes: %s", notes)
	}

	// Server unreachable: unchanged hint, no panic.
	down := func() ([]client.ProjectMeta, error) { return nil, errors.New("connection refused") }
	notes = strings.Join(bindTrackerProjectKeyWith(s2, other, down), "\n")
	if !strings.Contains(notes, "未匹配到") {
		t.Fatalf("down notes: %s", notes)
	}
}
