package commands

import (
	"github.com/inhere/gofer/internal/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestTrackerClientUsesDotenvServerWithoutFlag(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	t.Setenv("GOFER_SERVER_ADDR", "")
	t.Setenv("GOFER_SERVER_TOKEN", "")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GOFER_SERVER_ADDR=127.0.0.1:12345\nGOFER_SERVER_TOKEN=tok\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _ = config.LoadDotenv()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer ts.Close()
	t.Setenv("GOFER_SERVER_ADDR", ts.URL)
	cli, err := trackerClient()
	if err != nil {
		t.Fatal(err)
	}
	if cli.BaseURL() == "" {
		t.Fatal("empty client base url")
	}
}
