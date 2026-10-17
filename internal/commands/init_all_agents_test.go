package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/buildinfo"
)

func runInitTarget(t *testing.T, target, agent string) (string, error) {
	t.Helper()
	c := bindCmd(NewInitCmd(buildinfo.Info{}))
	c.Arg("target").WithValue(target)
	initOpts.config, initOpts.global, initOpts.force, initOpts.agent = "", true, false, agent
	t.Cleanup(func() {
		initOpts.config, initOpts.global, initOpts.force, initOpts.agent = "", false, false, "claude"
	})
	var err error
	out := captureOutput(t, func() { err = runInit(c, buildinfo.Info{}) })
	return out, err
}

func isolateHome(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("JCODE_HOME", "")
	return home
}

func missing(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); err == nil {
		t.Errorf("%s must not exist", p)
	}
}

// `init hook -a all` writes only for agents whose config dir exists and lists the rest.
func TestInitHookAllSkipsMissingAgentDirs(t *testing.T) {
	home := isolateHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := runInitTarget(t, "hook", "all")
	if err != nil {
		t.Fatalf("init hook -a all: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err != nil {
		t.Errorf("claude hooks not written: %v", err)
	}
	missing(t, filepath.Join(home, ".codex"))
	missing(t, filepath.Join(home, ".omp"))
	missing(t, filepath.Join(home, ".jcode"))
	for _, want := range []string{
		"跳过 codex：未发现 " + filepath.Join(home, ".codex"),
		"跳过 omp：未发现 " + filepath.Join(home, ".omp", "agent"),
		"跳过 jcode：未发现 " + filepath.Join(home, ".jcode"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// A named agent keeps creating its dir.
func TestInitHookNamedAgentCreatesDir(t *testing.T) {
	home := isolateHome(t)
	if _, err := runInitTarget(t, "hook", "codex"); err != nil {
		t.Fatalf("init hook -a codex: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "hooks.json")); err != nil {
		t.Errorf("codex hooks not written: %v", err)
	}
}

// `init skill -a all` only fills the convention dirs that exist.
func TestInitSkillAllSkipsMissingDirs(t *testing.T) {
	home := isolateHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := runInitTarget(t, "skill", "all")
	if err != nil {
		t.Fatalf("init skill -a all: %v", err)
	}
	assertSkillTree(t, filepath.Join(home, ".agents", "skills"))
	missing(t, filepath.Join(home, ".claude"))
	if want := "跳过：未发现 " + filepath.Join(home, ".claude"); !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
}

// With none of the dirs present, -a all writes nothing and succeeds.
func TestInitSkillAllNoneExist(t *testing.T) {
	home := isolateHome(t)
	out, err := runInitTarget(t, "skill", "all")
	if err != nil {
		t.Fatalf("init skill -a all: %v", err)
	}
	missing(t, filepath.Join(home, ".claude"))
	missing(t, filepath.Join(home, ".agents"))
	if strings.Count(out, "跳过：未发现") != 2 {
		t.Errorf("want both dirs listed as skipped:\n%s", out)
	}
}

// A named agent (the default) keeps creating both convention dirs.
func TestInitSkillNamedAgentCreatesDirs(t *testing.T) {
	home := isolateHome(t)
	if _, err := runInitTarget(t, "skill", "claude"); err != nil {
		t.Fatalf("init skill -a claude: %v", err)
	}
	assertSkillTree(t, filepath.Join(home, ".claude", "skills"))
	assertSkillTree(t, filepath.Join(home, ".agents", "skills"))
}
