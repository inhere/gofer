package serve

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestReloadResultPathAndLogLine(t *testing.T) {
	path := reloadResultPath(`C:\\gofer\\config.yaml`)
	if filepath.Base(filepath.Dir(path)) != "run" || filepath.Base(path) != "serve.reload.json" {
		t.Fatalf("path=%q, want run\\serve.reload.json", path)
	}
	line := formatReloadResult(config.ReloadResult{Rev: 3, Path: "config.yaml", Changed: []string{"server"}, RestartRequired: []string{"server.addr"}})
	for _, part := range []string{"rev=3", "path=config.yaml", "changed=server", "restart_required=server.addr"} {
		if !strings.Contains(line, part) {
			t.Fatalf("line=%q missing %q", line, part)
		}
	}
}
