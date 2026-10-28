package job

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

func TestResolveReadDirsProjectAndAllowList(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "docs")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	cfg := &config.Config{Server: config.ServerConfig{ReadDirAllow: []string{outside}}}
	proj := config.ProjectConfig{HostPath: root}
	got, err := resolveReadDirs(cfg, proj, []string{"docs", outside})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if _, err := resolveReadDirs(cfg, proj, []string{"../"}); err == nil {
		t.Fatal("expected traversal rejection")
	}
	if _, err := resolveReadDirs(cfg, proj, []string{filepath.Join(root, "missing")}); err == nil {
		t.Fatal("expected missing rejection")
	}
}

func TestReadDirArgs(t *testing.T) {
	ac := config.AgentConfig{Type: agent.TypeCLIAgent, AddDirArgs: []string{"--add-dir", "{{dir}}:ro"}}
	got := readDirArgs(ac, []string{"C:/docs", "C:/other"})
	want := []string{"--add-dir", "C:/docs:ro", "--add-dir", "C:/other:ro"}
	if len(got) != len(want) {
		t.Fatalf("got %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v want %#v", got, want)
		}
	}
}
