package commands

import (
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestServeRuntimeDirFollowsConfigFlag(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "nested", "serve.yaml")
	config.InputCfgFile = cfgPath
	t.Cleanup(func() { config.InputCfgFile = "" })

	wantDir := filepath.Join(filepath.Dir(cfgPath), "run")
	if got := filepath.Dir(servePIDFile()); got != wantDir {
		t.Fatalf("serve pid runtime dir = %q, want %q", got, wantDir)
	}
	if got := filepath.Dir(serveLogFile()); got != wantDir {
		t.Fatalf("serve log runtime dir = %q, want %q", got, wantDir)
	}
}
