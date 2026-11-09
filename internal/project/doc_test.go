package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// docFixture builds a project root with a spread of files; outside is a sibling
// dir (not inside root) holding a secret.
type docFixture struct {
	root, outside string
	cfg           *config.Config
	proj          config.ProjectConfig
}

func writeDocFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mustSymlink fails (never skips) when symlinks cannot be created on a non-Windows
// host; on Windows without the privilege the test is skipped with the reason.
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if filepath.Separator == '\\' {
			t.Skipf("symlink not permitted on this Windows host: %v", err)
		}
		t.Fatalf("symlink: %v", err)
	}
}

func newDocFixture(t *testing.T) *docFixture {
	t.Helper()
	top := t.TempDir()
	root := filepath.Join(top, "proj")
	outside := filepath.Join(top, "outside")
	writeDocFile(t, filepath.Join(root, "README.md"), "# root readme\n")
	writeDocFile(t, filepath.Join(root, "docs", "a.md"), "# a\n")
	writeDocFile(t, filepath.Join(root, "docs", "UP.MD"), "# upper\n")
	writeDocFile(t, filepath.Join(root, "docs", "b.markdown"), "# b\n")
	writeDocFile(t, filepath.Join(root, "docs", "dup.md"), "root dup\n")
	writeDocFile(t, filepath.Join(root, "sub", "docs", "dup.md"), "sub dup\n")
	writeDocFile(t, filepath.Join(root, "sub", "only-sub.md"), "only sub\n")
	writeDocFile(t, filepath.Join(root, ".env.md"), "SECRET\n")
	writeDocFile(t, filepath.Join(root, ".git", "x.md"), "SECRET\n")
	writeDocFile(t, filepath.Join(root, "docs", ".hidden", "x.md"), "SECRET\n")
	writeDocFile(t, filepath.Join(root, ".worktrees", "a", "w.md"), "SECRET\n")
	writeDocFile(t, filepath.Join(root, "x.txt"), "SECRET\n")
	writeDocFile(t, filepath.Join(root, "x.md.bak"), "SECRET\n")
	writeDocFile(t, filepath.Join(root, "x"), "SECRET\n")
	writeDocFile(t, filepath.Join(root, "bin.md"), "ab\x00cd")
	if err := os.MkdirAll(filepath.Join(root, "d.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDocFile(t, filepath.Join(outside, "secret.md"), "SECRET\n")
	writeDocFile(t, filepath.Join(outside, "dir", "x.md"), "SECRET\n")
	mustSymlink(t, filepath.Join(outside, "secret.md"), filepath.Join(root, "escape.md"))
	mustSymlink(t, filepath.Join(outside, "dir"), filepath.Join(root, "linkdir"))
	mustSymlink(t, filepath.Join(root, "docs", "a.md"), filepath.Join(root, "inlink.md"))
	cfg, proj := cfgFor(root)
	return &docFixture{root: root, outside: outside, cfg: cfg, proj: proj}
}

func TestReadDoc_OK(t *testing.T) {
	f := newDocFixture(t)
	tests := []struct {
		name, base, rel, wantPath, wantSub string
	}{
		{"root hit", "", "docs/a.md", "docs/a.md", "# a"},
		{"base dot is root", ".", "docs/a.md", "docs/a.md", "# a"},
		{"upper ext", "", "docs/UP.MD", "docs/UP.MD", "# upper"},
		{"markdown ext", "", "docs/b.markdown", "docs/b.markdown", "# b"},
		{"base wins over root", "sub", "docs/dup.md", "sub/docs/dup.md", "sub dup"},
		{"base miss falls back to root", "sub", "docs/a.md", "docs/a.md", "# a"},
		{"base only", "sub", "only-sub.md", "sub/only-sub.md", "only sub"},
		{"root dup without base", "", "docs/dup.md", "docs/dup.md", "root dup"},
		{"symlink inside project", "", "inlink.md", "inlink.md", "# a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dc, err := ReadDoc(f.cfg, f.proj, tc.base, tc.rel)
			if err != nil {
				t.Fatalf("ReadDoc: %v", err)
			}
			if dc.Path != tc.wantPath || !strings.Contains(dc.Content, tc.wantSub) || dc.Truncated {
				t.Fatalf("got %+v, want path %q containing %q", dc, tc.wantPath, tc.wantSub)
			}
		})
	}
}

func TestReadDoc_RootBehindSymlink(t *testing.T) {
	f := newDocFixture(t)
	link := filepath.Join(t.TempDir(), "rootlink")
	mustSymlink(t, f.root, link)
	cfg, proj := cfgFor(link)
	dc, err := ReadDoc(cfg, proj, "", "docs/a.md")
	if err != nil || !strings.Contains(dc.Content, "# a") {
		t.Fatalf("root via symlink: %+v %v", dc, err)
	}
}

func TestReadDoc_Truncation(t *testing.T) {
	f := newDocFixture(t)
	writeDocFile(t, filepath.Join(f.root, "exact.md"), strings.Repeat("a", maxKeyFileBytes))
	writeDocFile(t, filepath.Join(f.root, "big.md"), strings.Repeat("a", maxKeyFileBytes+1))
	dc, err := ReadDoc(f.cfg, f.proj, "", "exact.md")
	if err != nil || dc.Truncated || len(dc.Content) != maxKeyFileBytes {
		t.Fatalf("exact: truncated=%v len=%d err=%v", dc.Truncated, len(dc.Content), err)
	}
	dc, err = ReadDoc(f.cfg, f.proj, "", "big.md")
	if err != nil || !dc.Truncated || len(dc.Content) != maxKeyFileBytes || dc.Size != maxKeyFileBytes+1 {
		t.Fatalf("big: truncated=%v len=%d size=%d err=%v", dc.Truncated, len(dc.Content), dc.Size, err)
	}
}

func TestReadDoc_Forbidden(t *testing.T) {
	f := newDocFixture(t)
	tests := []struct{ name, base, rel string }{
		{"empty", "", ""},
		{"dotdot", "", "../x.md"},
		{"inner dotdot", "", "a/../../x.md"},
		{"abs posix", "", "/etc/x.md"},
		{"drive slash", "", "C:/x.md"},
		{"drive rel", "", "C:x.md"},
		{"unc", "", `\\srv\x.md`},
		{"backslash", "", `a\b.md`},
		{"ads colon", "", "secret:x.md"},
		{"nul in rel", "", "a\x00.md"},
		{"nul in base", "a\x00", "docs/a.md"},
		{"dot env", "", ".env.md"},
		{"dot git", "", ".git/x.md"},
		{"hidden dir", "", "docs/.hidden/x.md"},
		{"base worktrees", ".worktrees/a", "w.md"},
		{"base dotdot", "../outside", "secret.md"},
		{"base abs", "/tmp", "x.md"},
		{"base colon", "C:", "docs/a.md"},
		{"base backslash", `a\b`, "docs/a.md"},
		{"txt", "", "x.txt"},
		{"md bak", "", "x.md.bak"},
		{"no ext", "", "x"},
		{"trailing slash", "", "docs/a.md/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dc, err := ReadDoc(f.cfg, f.proj, tc.base, tc.rel)
			if !errors.Is(err, ErrForbiddenDoc) {
				t.Fatalf("err=%v content=%+v, want ErrForbiddenDoc", err, dc)
			}
			if dc.Content != "" {
				t.Fatalf("content leaked: %q", dc.Content)
			}
			assertNoPathLeak(t, f, err)
		})
	}
}

func TestReadDoc_NotFound(t *testing.T) {
	f := newDocFixture(t)
	tests := []struct{ name, base, rel string }{
		{"missing", "", "nope.md"},
		{"missing with base", "sub", "nope.md"},
		{"symlink file escape", "", "escape.md"},
		{"symlink dir escape", "", "linkdir/x.md"},
		{"symlink escape via base", "linkdir", "x.md"},
		{"directory named d.md", "", "d.md"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dc, err := ReadDoc(f.cfg, f.proj, tc.base, tc.rel)
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("err=%v content=%+v, want os.ErrNotExist", err, dc)
			}
			if dc.Content != "" {
				t.Fatalf("content leaked: %q", dc.Content)
			}
			assertNoPathLeak(t, f, err)
		})
	}
}

func TestReadDoc_BaseEscapeFallsBackToRoot(t *testing.T) {
	// base candidate escapes via a symlink dir, root candidate exists: root wins.
	f := newDocFixture(t)
	writeDocFile(t, filepath.Join(f.root, "x.md"), "root x\n")
	dc, err := ReadDoc(f.cfg, f.proj, "linkdir", "x.md")
	if err != nil || dc.Path != "x.md" || strings.Contains(dc.Content, "SECRET") {
		t.Fatalf("got %+v err=%v", dc, err)
	}
}

func TestReadDoc_Binary(t *testing.T) {
	f := newDocFixture(t)
	dc, err := ReadDoc(f.cfg, f.proj, "", "bin.md")
	if !errors.Is(err, ErrBinary) || dc.Content != "" {
		t.Fatalf("err=%v content=%q, want ErrBinary", err, dc.Content)
	}
	assertNoPathLeak(t, f, err)
}

func TestReadDoc_AllowedRunners(t *testing.T) {
	f := newDocFixture(t)
	tests := []struct {
		name    string
		runners []string
		wantErr error
	}{
		{"worker only", []string{"w1"}, ErrProjectNotLocal},
		{"empty means local", nil, nil},
		{"contains local", []string{"w1", "local"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proj := f.proj
			proj.AllowedRunners = tc.runners
			_, err := ReadDoc(f.cfg, proj, "", "docs/a.md")
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("err=%v, want %v", err, tc.wantErr)
			}
			if err != nil {
				assertNoPathLeak(t, f, err)
			}
		})
	}
}

func assertNoPathLeak(t *testing.T, f *docFixture, err error) {
	t.Helper()
	msg := err.Error()
	top := filepath.Dir(f.root)
	if strings.Contains(msg, f.root) || strings.Contains(msg, f.outside) || strings.Contains(msg, top) {
		t.Fatalf("error leaks an absolute path: %q", msg)
	}
}
