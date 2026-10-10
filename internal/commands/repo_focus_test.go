package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/tracker"
)

func TestPrimeFocusSectionAndToggle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/runners" {
			_ = json.NewEncoder(w).Encode(map[string]any{"server": map[string]string{"version": "9.9.9"}, "runners": []any{}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	t.Setenv("GOFER_SERVER_ADDR", srv.URL)
	old := primeFocusGit
	primeFocusGit = func(context.Context, string, ...string) (string, error) { return "", errors.New("no git") }
	defer func() { primeFocusGit = old }()

	s, _, err := tracker.Init(t.TempDir(), "prime", true)
	assert.NoErr(t, err)
	body, err := primeWithServerContext(s, "", "")
	assert.NoErr(t, err)
	assert.StrContains(t, body, "## 当前重点（自动，")
	assert.StrContains(t, body, "- 服务：server 9.9.9\n")
	assert.NotContains(t, body, "仓库：")

	cfg, err := s.ReadConfig()
	assert.NoErr(t, err)
	off := false
	cfg.Prime.Focus = &off
	data, err := yaml.Marshal(cfg)
	assert.NoErr(t, err)
	assert.NoErr(t, os.WriteFile(filepath.Join(s.Dir, "config.yaml"), data, 0o644))
	body, err = primeWithServerContext(s, "", "")
	assert.NoErr(t, err)
	assert.NotContains(t, body, "当前重点")
}

// TestPrimeFocusListsOnlyThisProjectsWorkers: the server line counts only the
// workers this project may dispatch to; other projects' worker names never reach
// the prime, and a version mismatch is a count, not a name.
func TestPrimeFocusListsOnlyThisProjectsWorkers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/runners":
			_ = json.NewEncoder(w).Encode(map[string]any{"server": map[string]string{"version": "2.0.0"}, "runners": []any{
				map[string]any{"name": "local", "type": "local", "status": "up"},
				map[string]any{"name": "mine-a", "type": "worker", "status": "connected", "worker": map[string]string{"gofer_version": "2.0.0"}},
				map[string]any{"name": "mine-b", "type": "worker", "status": "connected", "worker": map[string]string{"gofer_version": "1.9.0"}},
				map[string]any{"name": "secret-box", "type": "worker", "status": "connected", "worker": map[string]string{"gofer_version": "1.0.0"}},
				map[string]any{"name": "secret-off", "type": "worker", "status": "disconnected"},
			}})
		case "/v1/meta":
			_ = json.NewEncoder(w).Encode(map[string]any{"projects": []any{
				map[string]any{"key": "proj", "allowed_runners": []string{"local", "mine-a", "mine-b"}},
				map[string]any{"key": "other", "allowed_runners": []string{"secret-box", "secret-off"}},
			}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Setenv("GOFER_SERVER_ADDR", srv.URL)
	old := primeFocusGit
	primeFocusGit = func(context.Context, string, ...string) (string, error) { return "", errors.New("no git") }
	defer func() { primeFocusGit = old }()

	s, _, err := tracker.Init(t.TempDir(), "prime", true)
	assert.NoErr(t, err)
	assert.NoErr(t, s.SetProjectKey("proj"))
	body, err := primeWithServerContext(s, "", "")
	assert.NoErr(t, err)
	assert.StrContains(t, body, "- 服务：server 2.0.0；本项目 worker 2/2 在线，1 个与 server 版本不同\n")
	for _, leaked := range []string{"secret-box", "secret-off", "mine-b"} {
		assert.NotContains(t, body, leaked)
	}

	// prime.focus_env=false: no server line at all
	assert.NoErr(t, s.UpdateConfig(func(c *tracker.Config) { off := false; c.Prime.FocusEnv = &off }))
	body, err = primeWithServerContext(s, "", "")
	assert.NoErr(t, err)
	assert.NotContains(t, body, "服务：")
}
