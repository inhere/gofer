package commands

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tracker"
)

// TestRepoSyncManualOutlivesAutoSyncTimeout: a manual `repo sync` of a large tracker
// (first sync of ~1000 issues takes several seconds) must not be cut off by the 2s
// timeout that only exists so automatic sync never stalls a write command.
func TestRepoSyncManualOutlivesAutoSyncTimeout(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("GOFER_SERVER_ADDR", "")
	t.Setenv("GOFER_SERVER_TOKEN", "")
	if _, _, err := tracker.Init(root, "slow", true); err != nil {
		t.Fatalf("init tracker: %v", err)
	}
	var hit bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/tracker/sync" {
			hit = true
			time.Sleep(2500 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("server:\n  addr: %q\n  allow_empty_token: true\n", server.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	old := config.InputCfgFile
	config.InputCfgFile = configPath
	t.Cleanup(func() { config.InputCfgFile = old })

	def := findSub(t, NewRepoCmd(), "sync")
	c := bindCmd(def)
	err := def.Func(c, nil)
	if !hit {
		t.Fatalf("sync never reached the server (err=%v)", err)
	}
	if err != nil && strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("manual sync was cut off by the auto-sync timeout: %v", err)
	}
}
