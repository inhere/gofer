package commands

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// writeSkillTree lays out a source skill directory (relative slash paths → content).
func writeSkillTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
}

// zipEntryText returns one entry's content, failing the test when it is absent.
func zipEntryText(t *testing.T, archive, name string) string {
	t.Helper()
	zr, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatalf("open %s: %v", archive, err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s in %s: %v", name, archive, err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read %s in %s: %v", name, archive, err)
		}
		return string(data)
	}
	t.Fatalf("archive %s has no entry %q", archive, name)
	return ""
}

// TestAgentSkillCommands drives the whole `agent skill` surface in LOCAL mode — no
// server, no HTTP — over a temp config dir: import a directory skill, list it, show
// its SKILL.md body, export it as a zip, then remove it and watch it disappear. It
// also pins the failing exit of a read of an unknown skill.
func TestAgentSkillCommands(t *testing.T) {
	isolateConfigEnv(t)
	// The local path is what this test covers; a client-mode env in the shell must
	// not silently route it to a server.
	t.Setenv(config.EnvRunMode, "server")

	cfgPath := writeRawConfig(t, "# gofer skill CLI test\n")
	config.InputCfgFile = cfgPath
	t.Cleanup(func() { config.InputCfgFile = "" })
	agentSkillOpts.local, agentSkillOpts.out = false, ""
	t.Cleanup(func() { agentSkillOpts.local, agentSkillOpts.out = false, "" })

	body := "Use apply_patch, never PowerShell here-strings.\n"
	src := filepath.Join(t.TempDir(), "src")
	writeSkillTree(t, src, map[string]string{
		"SKILL.md":     "---\nname: house-rules\ndescription: house working rules\n---\n\n# House rules\n\n" + body,
		"ref/notes.md": "# notes\n",
	})

	// run drives the real gcli app; -c is bound per subcommand (P1.5), so it may sit
	// after the command name.
	run := func(args ...string) (string, int) {
		t.Helper()
		args = append(args, "-c", cfgPath)
		var code int
		out := captureOutput(t, func() { code = NewApp("test").Run(args) })
		return out, code
	}

	if out, code := run("agent", "skill", "import", src); code != 0 {
		t.Fatalf("import exit code=%d, out=%s", code, out)
	} else if !strings.Contains(out, "imported skill house-rules") {
		t.Fatalf("import output should name the skill, got: %s", out)
	}

	out, code := run("agent", "skill", "ls")
	if code != 0 {
		t.Fatalf("ls exit code=%d, out=%s", code, out)
	}
	if !strings.Contains(out, "house-rules") || !strings.Contains(out, "house working rules") {
		t.Fatalf("ls should list the imported skill with its description, got: %s", out)
	}

	out, code = run("agent", "skill", "show", "house-rules")
	if code != 0 {
		t.Fatalf("show exit code=%d, out=%s", code, out)
	}
	if !strings.Contains(out, body) {
		t.Fatalf("show should print the SKILL.md body, got: %s", out)
	}
	if !strings.Contains(out, "ref/notes.md") {
		t.Fatalf("show should list every file, got: %s", out)
	}

	archive := filepath.Join(t.TempDir(), "house-rules-export.zip")
	out, code = run("agent", "skill", "export", "house-rules", "--out", archive)
	if code != 0 {
		t.Fatalf("export exit code=%d, out=%s", code, out)
	}
	if got := zipEntryText(t, archive, "SKILL.md"); !strings.Contains(got, body) {
		t.Fatalf("exported SKILL.md = %q, want the skill's own content", got)
	}
	if got := zipEntryText(t, archive, "ref/notes.md"); got != "# notes\n" {
		t.Fatalf("exported ref/notes.md = %q, want the source bytes", got)
	}

	// An unknown skill is a failing read, not an empty print.
	if out, code := run("agent", "skill", "show", "ghost"); code == 0 {
		t.Fatalf("show of an unknown skill should exit non-zero, out=%s", out)
	}

	out, code = run("agent", "skill", "rm", "house-rules")
	if code != 0 {
		t.Fatalf("rm exit code=%d, out=%s", code, out)
	}
	out, code = run("agent", "skill", "ls")
	if code != 0 {
		t.Fatalf("ls after rm exit code=%d, out=%s", code, out)
	}
	if strings.Contains(out, "house-rules") || !strings.Contains(out, "(no skills)") {
		t.Fatalf("ls after rm should be empty, got: %s", out)
	}
}
