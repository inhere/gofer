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
