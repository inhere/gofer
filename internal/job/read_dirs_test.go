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

func TestResolveReadDirsRejectsFileAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{}
	proj := config.ProjectConfig{HostPath: root}
	file := filepath.Join(root, "note.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveReadDirs(cfg, proj, []string{"note.txt"}); err == nil {
		t.Fatal("expected non-directory rejection")
	}

	outside := t.TempDir()
	link := filepath.Join(root, "outside")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := resolveReadDirs(cfg, proj, []string{"outside"}); err == nil {
		t.Fatal("expected symlink escape rejection")
	}
}

func TestValidateReadDirAgent(t *testing.T) {
	req := JobRequest{Agent: "a", ReadDirs: []string{"docs"}}
	for name, ac := range map[string]config.AgentConfig{
		"missing add_dir_args": {Type: agent.TypeCLIAgent},
		"acp-agent":            {Type: agent.TypeACPAgent, AddDirArgs: []string{"--add-dir", "{{dir}}"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateReadDirAgent(req, ac); err == nil {
				t.Fatal("expected read_dirs capability rejection")
			}
		})
	}
}
