package commands

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestClientRequestLogStaysInConfigDir drives the REAL entry path (assembly +
// app.Run): assembling the app points the client request log at
// <config-dir>/run/client.log, and when the user's config dir cannot be resolved
// there is no log file at all — least of all ./run/client.log in the directory the
// command ran in, which inside a job is the business repository.
func TestClientRequestLogStaysInConfigDir(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	t.Run("written under the config dir", func(t *testing.T) {
		cfg := t.TempDir()
		t.Setenv(config.EnvConfigDir, cfg)
		var code int
		out := captureOutput(t, func() {
			code = NewApp("test").Run([]string{"job", "show", "job-1", "--server", srv.URL})
		})
		if code == 0 {
			t.Fatalf("want a failing command, output=%q", out)
		}
		if !strings.Contains(out, "502") {
			t.Fatalf("request did not reach the server: %q", out)
		}
		logPath := filepath.Join(cfg, "run", "client.log")
		raw, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("client log not written to %s: %v", logPath, err)
		}
		if !strings.Contains(string(raw), "client.request_failed") || !strings.Contains(string(raw), `"status":502`) {
			t.Fatalf("client log content = %q", raw)
		}
	})

	t.Run("no file when the config dir is unresolvable", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		t.Setenv(config.EnvConfigDir, "")
		t.Setenv("HOME", "")
		t.Setenv("USERPROFILE", "")
		if _, err := config.ConfigDir(); err == nil {
			t.Fatalf("fixture: ConfigDir must be unresolvable here")
		}
		var code int
		out := captureOutput(t, func() {
			code = NewApp("test").Run([]string{"job", "show", "job-1", "--server", srv.URL})
		})
		if code == 0 {
			t.Fatalf("want a failing command, output=%q", out)
		}
		if !strings.Contains(out, "502") {
			t.Fatalf("request did not reach the server: %q", out)
		}
		if _, err := os.Stat(filepath.Join(dir, "run")); !os.IsNotExist(err) {
			t.Fatalf("./run must not be created in the cwd (stat err=%v)", err)
		}
	})
}
