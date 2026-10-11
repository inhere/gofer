package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

// A config split with include reloads from every file: an edit to a fragment is
// applied, and the receipt lists the files that were read.
func TestReloadReadsIncludedFragments(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	main := filepath.Join(dir, "config.yaml")
	frag := filepath.Join(dir, "config.d", "agents.yaml")
	if err := os.MkdirAll(filepath.Dir(frag), 0o755); err != nil {
		t.Fatal(err)
	}
	mainText := "include: [config.d/*.yaml]\nserver:\n  addr: 127.0.0.1:1\nstorage:\n  root: " + filepath.ToSlash(root) + "\n"
	if err := os.WriteFile(main, []byte(mainText), 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(name string) {
		body := "agents:\n  " + name + ":\n    type: cli-agent\n    command: " + name + "\n    args: [\"{{prompt}}\"]\n"
		if err := os.WriteFile(frag, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("alpha")
	cfg, _, err := config.Load(main)
	if err != nil {
		t.Fatal(err)
	}
	cr, err := Build(cfg, WithConfigPath(main), WithAgentDetector(agent.NoopDetector{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cr.Close() }()

	write("beta")
	report, err := cr.ReloadDetailed(main)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Files) != 2 || report.Files[0] != main || report.Files[1] != frag {
		t.Fatalf("files=%v, want [main, fragment]", report.Files)
	}
	if !strings.Contains(strings.Join(report.Changed, ","), "agents") {
		t.Fatalf("changed=%v, want agents", report.Changed)
	}
	if _, ok := cr.Snapshot().Cfg.Agents["beta"]; !ok {
		t.Fatalf("fragment edit not applied: agents=%v", cr.Snapshot().Cfg.Agents)
	}
}
