package skill

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// memRepo is the library index a unit test drives: the Store's persistence seam
// without SQLite, so an import can be asserted on its own.
type memRepo struct{ m map[string]Skill }

func newMemRepo() *memRepo { return &memRepo{m: map[string]Skill{}} }

func (r *memRepo) InsertSkill(s Skill) error { r.m[s.Name] = s; return nil }
func (r *memRepo) UpdateSkill(s Skill) error { r.m[s.Name] = s; return nil }
func (r *memRepo) GetSkill(name string) (Skill, bool, error) {
	s, ok := r.m[name]
	return s, ok, nil
}
func (r *memRepo) ListSkills() ([]Skill, error) {
	out := make([]Skill, 0, len(r.m))
	for _, s := range r.m {
		out = append(out, s)
	}
	return out, nil
}
func (r *memRepo) DeleteSkill(name string) error { delete(r.m, name); return nil }

// newTestStore builds a Store over a temp root with the given limits.
func newTestStore(t *testing.T, lim Limits) (*Store, *memRepo) {
	t.Helper()
	repo := newMemRepo()
	s, err := NewStore(filepath.Join(t.TempDir(), "skills"), repo, lim)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s, repo
}

// writeSkillDir writes a skill source dir and returns its path.
func writeSkillDir(t *testing.T, name, desc string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return dir
}

// zipSkill packs a skill dir into a .zip on disk and returns its path.
func zipSkill(t *testing.T, src string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		w, cerr := zw.Create(filepath.ToSlash(rel))
		if cerr != nil {
			return cerr
		}
		_, werr := w.Write(b)
		return werr
	})
	if err != nil {
		t.Fatalf("zip walk: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	out := filepath.Join(t.TempDir(), "skill.zip")
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write zip: %v", err)
	}
	return out
}

// TestSkillImportFromDirAndZip: a skill comes in from a plain directory OR a .zip,
// and the library ends up with the name/description from SKILL.md's frontmatter and
// a sha256 per file — the index the bindings and the change detection read.
func TestSkillImportFromDirAndZip(t *testing.T) {
	for _, kind := range []string{"dir", "zip"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := newTestStore(t, DefaultLimits())
			src := writeSkillDir(t, "windows-apply-patch", "how to edit files on windows",
				map[string]string{"ref/notes.md": "hello"})
			arg := src
			if kind == "zip" {
				arg = zipSkill(t, src)
			}
			got, err := s.Import(arg, "tester")
			if err != nil {
				t.Fatalf("Import(%s): %v", kind, err)
			}
			if got.Name != "windows-apply-patch" {
				t.Fatalf("name = %q, want windows-apply-patch", got.Name)
			}
			if got.Description != "how to edit files on windows" {
				t.Fatalf("description = %q, want the SKILL.md frontmatter value", got.Description)
			}
			if len(got.Files) != 2 {
				t.Fatalf("files = %d, want 2 (SKILL.md + ref/notes.md)", len(got.Files))
			}
			for _, f := range got.Files {
				if len(f.SHA256) != 64 {
					t.Fatalf("file %s: sha256 = %q, want 64 hex chars", f.Path, f.SHA256)
				}
				if f.Path == "ref/notes.md" {
					sum := sha256.Sum256([]byte("hello"))
					if want := hex.EncodeToString(sum[:]); f.SHA256 != want {
						t.Fatalf("ref/notes.md sha256 = %s, want %s (the sha256 of its bytes)", f.SHA256, want)
					}
				}
			}
			// The files are on disk under the store root, and readable.
			if _, err := os.Stat(filepath.Join(s.Dir("windows-apply-patch"), "SKILL.md")); err != nil {
				t.Fatalf("imported SKILL.md: %v", err)
			}
		})
	}

	// A source without SKILL.md is not a skill.
	t.Run("missing SKILL.md rejected", func(t *testing.T) {
		s, _ := newTestStore(t, DefaultLimits())
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("nope"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Import(dir, "tester"); err == nil {
			t.Fatal("Import of a dir without SKILL.md succeeded, want ErrInvalid")
		}
	})
}

// TestSkillImportRejectsEscape: a zip that tries to write outside the skill dir —
// `../x`, an absolute path, or a symlink — is refused and leaves nothing behind on
// disk (an archive is untrusted input, and this is the boundary that keeps it in).
func TestSkillImportRejectsEscape(t *testing.T) {
	evil := func(name string) string {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, err := zw.Create("SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte("---\nname: evil\n---\n"))
		switch name {
		case "dotdot":
			w, _ = zw.Create("../x.txt")
		case "abs":
			w, _ = zw.Create("/abs-x.txt")
		case "symlink":
			h := &zip.FileHeader{Name: "link"}
			h.SetMode(os.ModeSymlink | 0o777)
			w, _ = zw.CreateHeader(h)
		}
		_, _ = w.Write([]byte("pwned"))
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), "evil.zip")
		if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	for _, name := range []string{"dotdot", "abs", "symlink"} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "skills")
			s, err := NewStore(root, newMemRepo(), DefaultLimits())
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			parent := filepath.Dir(root)
			if _, err := s.Import(evil(name), "tester"); err == nil {
				t.Fatalf("Import of a %s archive succeeded, want a refusal", name)
			}
			// No residue: nothing named "evil" under the store root, and the sibling
			// files the escape tried to create do not exist.
			if _, err := os.Stat(filepath.Join(root, "evil")); err == nil {
				t.Fatal("a rejected import left the skill dir behind")
			}
			for _, leaked := range []string{
				filepath.Join(parent, "x.txt"),
				filepath.Join(parent, "abs-x.txt"),
				filepath.Join(root, "evil", "link"),
			} {
				if _, err := os.Stat(leaked); err == nil {
					t.Fatalf("a rejected import wrote %s", leaked)
				}
			}
		})
	}
}

// TestSkillImportStripsExecBit: a skill is knowledge, not a program — an exec bit
// arriving in the source does not survive the import (the agent runs scripts by
// naming an interpreter, exactly like any other file it reads).
func TestSkillImportStripsExecBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows has no unix permission bits: the x-bit assertion only means something on unix")
	}
	s, _ := newTestStore(t, DefaultLimits())
	src := writeSkillDir(t, "runnable", "has a script", nil)
	script := filepath.Join(src, "run.sh")
	if err := os.WriteFile(script, []byte("echo hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(src, "tester"); err != nil {
		t.Fatalf("Import: %v", err)
	}
	info, err := os.Stat(filepath.Join(s.Dir("runnable"), "run.sh"))
	if err != nil {
		t.Fatalf("stat imported script: %v", err)
	}
	if info.Mode().Perm()&0o111 != 0 {
		t.Fatalf("imported script mode = %v, want no exec bit", info.Mode().Perm())
	}
}

// TestSkillImportSizeLimits: the byte caps are enforced per file and in total, with
// the offending file named — a skill is prompt material, so a runaway archive is
// refused at the door instead of landing in the library.
func TestSkillImportSizeLimits(t *testing.T) {
	t.Run("per file", func(t *testing.T) {
		s, _ := newTestStore(t, Limits{MaxFileBytes: 64, MaxTotalBytes: 4096})
		src := writeSkillDir(t, "big", "one big file", map[string]string{"big.txt": strings.Repeat("x", 128)})
		_, err := s.Import(src, "tester")
		if err == nil {
			t.Fatal("import with a file over max_file_bytes succeeded")
		}
		if !strings.Contains(err.Error(), "big.txt") {
			t.Fatalf("error = %v, want the offending file named", err)
		}
	})
	t.Run("total", func(t *testing.T) {
		s, _ := newTestStore(t, Limits{MaxFileBytes: 1024, MaxTotalBytes: 128})
		src := writeSkillDir(t, "wide", "many files", map[string]string{
			"a.txt": strings.Repeat("y", 128),
			"b.txt": strings.Repeat("z", 128),
		})
		if _, err := s.Import(src, "tester"); err == nil {
			t.Fatal("import over max_total_bytes succeeded")
		}
	})
}
