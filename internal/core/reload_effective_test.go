package core

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// The reload receipt compares effective values (gofer-n6ax): deleting keys that only
// restated a default, rewriting an agent with `extends: builtin` and moving sections
// into include files changes no behaviour, so nothing is reported; a real edit still is.
func TestReloadReceiptComparesEffectiveValues(t *testing.T) {
	tpl, ok := config.BuiltinAgentTemplate("claude")
	if !ok {
		t.Fatal("claude template not registered")
	}
	spelled, err := config.RenderYAML(map[string]any{"agents": map[string]any{
		"claude": tpl,
		"exec":   map[string]any{"type": "exec"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	load := func(files map[string]string) *config.Config {
		t.Helper()
		dir := t.TempDir()
		for name, body := range files {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		cfg, _, err := config.Load(filepath.Join(dir, "config.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	oldCfg := load(map[string]string{"config.yaml": "server:\n  addr: 127.0.0.1:1\n  web_enabled: true\n" +
		"log:\n  max_size_mb: 50\n  max_age_days: 14\n" +
		"storage:\n  default_exchange_subdir: " + config.DefaultExchangeSubdir + "\n" + spelled})
	newCfg := load(map[string]string{
		"config.yaml":          "include: [config.d/*.yaml]\nserver:\n  addr: 127.0.0.1:1\n",
		"config.d/agents.yaml": "agents:\n  claude:\n    extends: builtin\n",
	})
	if got := reloadResult(oldCfg, newCfg, "config.yaml"); len(got.Changed) != 0 || len(got.RestartRequired) != 0 {
		t.Fatalf("no effective change, got changed=%v restart_required=%v", got.Changed, got.RestartRequired)
	}

	edited := load(map[string]string{"config.yaml": "server:\n  addr: 127.0.0.1:1\n  web_enabled: false\n" +
		"agents:\n  claude:\n    extends: builtin\n    args: [-p, \"{{prompt}}\"]\n"})
	got := reloadResult(newCfg, edited, "config.yaml")
	if !slices.Contains(got.RestartRequired, "server.web_enabled") || !slices.Contains(got.Changed, "agents") {
		t.Fatalf("real edits must be reported, got changed=%v restart_required=%v", got.Changed, got.RestartRequired)
	}
}
