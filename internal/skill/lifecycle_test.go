package skill

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lifecycle_test.go covers the operations import_test.go does not: the name a
// frontmatter-less source falls back to, the URL/git sources, Update's file-level
// diff, Export, Mount and Remove. Everything here is local and deterministic — the
// only "network" is an httptest server in the same process.

// seedDirSkill writes a skill directory WITHOUT frontmatter (so the name has to
// come from the source) and returns its path.
func seedDirSkill(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(dir, "ref"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# body only\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ref", "a.txt"), []byte("one"), 0o644); err != nil {
		t.Fatalf("write ref/a.txt: %v", err)
	}
	return dir
}

func mustReadFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return b
}

// TestSkillImportDirWithoutFrontmatter defends the fallback name: a SKILL.md with no
// frontmatter imports under the source directory's own basename, with the file index
// and total size computed from the tree.
func TestSkillImportDirWithoutFrontmatter(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits())
	src := seedDirSkill(t, "no-front")

	got, err := s.Import(src, "tester")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got.Name != "no-front" || got.Source != sourceDir || got.SourceRef != src {
		t.Fatalf("name/source = %q/%q/%q, want no-front/dir/%s", got.Name, got.Source, got.SourceRef, src)
	}
	if got.Version == "" || len(got.Files) != 2 || got.Size != int64(len("# body only\n")+3) {
		t.Fatalf("files=%v size=%d version=%q", got.Files, got.Size, got.Version)
	}
	if s.Dir(got.Name) == "" || s.Dir("BAD NAME") != "" {
		t.Fatalf("Dir: %q / %q, want a path then \"\" for an invalid name", s.Dir(got.Name), s.Dir("BAD NAME"))
	}
	if list, err := s.List(); err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}
	if _, ok, err := s.Get("nope"); err != nil || ok {
		t.Fatalf("Get(nope) = %v %v, want not-found", ok, err)
	}
}

// TestSkillUpdateReturnsFileDiff defends Update's contract: re-fetching the recorded
// source reports the added/changed/removed paths (by sha256) and a source that no
// longer validates leaves the library exactly as it was.
func TestSkillUpdateReturnsFileDiff(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits())
	src := seedDirSkill(t, "no-front")
	if _, err := s.Import(src, "tester"); err != nil {
		t.Fatalf("Import: %v", err)
	}

	// A source that lost its SKILL.md must fail AND leave the old skill intact.
	if err := os.Remove(filepath.Join(src, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update("no-front", "tester"); err == nil {
		t.Fatal("Update without SKILL.md succeeded, want a refusal")
	}
	if k, ok, _ := s.Get("no-front"); !ok || len(k.Files) != 2 {
		t.Fatalf("a failed update damaged the library: %v", k.Files)
	}

	// Now a real change: SKILL.md edited, ref/a.txt removed, new.md added.
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("# body v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "new.md"), []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(src, "ref", "a.txt")); err != nil {
		t.Fatal(err)
	}
	ch, err := s.Update("no-front", "tester")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if strings.Join(ch.Added, ",") != "new.md" {
		t.Fatalf("Added = %v, want [new.md]", ch.Added)
	}
	if strings.Join(ch.Removed, ",") != "ref/a.txt" {
		t.Fatalf("Removed = %v, want [ref/a.txt]", ch.Removed)
	}
	if strings.Join(ch.Changed, ",") != "SKILL.md" {
		t.Fatalf("Changed = %v, want [SKILL.md]", ch.Changed)
	}
}

// TestSkillUpdateWithoutSource defends the ErrInvalid path: a skill whose index entry
// records no source cannot be re-fetched and says so.
func TestSkillUpdateWithoutSource(t *testing.T) {
	s, repo := newTestStore(t, DefaultLimits())
	if err := repo.InsertSkill(Skill{Name: "orphan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update("orphan", "tester"); err == nil || !strings.Contains(err.Error(), "no recorded source") {
		t.Fatalf("Update(orphan) = %v, want the no-recorded-source error", err)
	}
}

// TestSkillExportReimportsIdentically defends Export's contract: the stream is a zip
// whose entries are relative to the skill dir, so re-importing it reproduces the same
// file set and version.
func TestSkillExportReimportsIdentically(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits())
	src := seedDirSkill(t, "no-front")
	if _, err := s.Import(src, "tester"); err != nil {
		t.Fatalf("Import: %v", err)
	}
	cur, ok, err := s.Get("no-front")
	if err != nil || !ok {
		t.Fatalf("Get: %v %v", ok, err)
	}

	var buf strings.Builder
	if err := s.Export("no-front", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}
	zp := filepath.Join(t.TempDir(), "exported.zip")
	if err := os.WriteFile(zp, []byte(buf.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := s.Import(zp, "tester")
	if err != nil {
		t.Fatalf("re-import of the export: %v", err)
	}
	// The name comes from the zip's own basename here (SKILL.md has no frontmatter);
	// what must not change is the content index.
	if again.Name != "exported" || again.Version != cur.Version || len(again.Files) != len(cur.Files) {
		t.Fatalf("round trip = %q/%s (%d files), want exported/%s (%d files)",
			again.Name, again.Version, len(again.Files), cur.Version, len(cur.Files))
	}
}

// TestSkillMountCopiesSkillIntoJobDir defends Mount: the skill lands in a caller
// chosen directory with its layout preserved and the byte count returned, and a
// destination that overlaps the skill dir (either direction) is refused.
func TestSkillMountCopiesSkillIntoJobDir(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits())
	if _, err := s.Import(seedDirSkill(t, "no-front"), "tester"); err != nil {
		t.Fatalf("Import: %v", err)
	}
	cur, _, err := s.Get("no-front")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "result-dir", "skills", "no-front")
	n, err := s.Mount("no-front", dst)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if n != cur.Size {
		t.Fatalf("Mount wrote %d bytes, want %d", n, cur.Size)
	}
	if _, err := os.Stat(filepath.Join(dst, "SKILL.md")); err != nil {
		t.Fatalf("mounted SKILL.md: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "ref", "a.txt")); err != nil {
		t.Fatalf("mounted ref/a.txt: %v", err)
	}
	if _, err := s.Mount("no-front", s.Dir("no-front")); err == nil {
		t.Fatal("Mount into the skill dir itself succeeded, want a refusal")
	}
	if _, err := s.Mount("no-front", s.Root()); err == nil {
		t.Fatal("Mount into the store root succeeded, want a refusal")
	}
	if _, err := s.Mount("nope", dst); err == nil {
		t.Fatal("Mount of an unknown skill succeeded, want a refusal")
	}
}

// TestSkillRemoveDropsIndexAndDir defends Remove: the index row and the directory are
// both gone afterwards, and removing it again is an error (the caller asked to delete
// something that is not there).
func TestSkillRemoveDropsIndexAndDir(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits())
	if _, err := s.Import(seedDirSkill(t, "no-front"), "tester"); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Remove("no-front"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok, _ := s.Get("no-front"); ok {
		t.Fatal("Remove left the index row behind")
	}
	if _, err := os.Stat(s.Dir("no-front")); !os.IsNotExist(err) {
		t.Fatalf("Remove left the directory behind: %v", err)
	}
	if err := s.Remove("no-front"); err == nil {
		t.Fatal("a second Remove succeeded, want not-found")
	}
}

// TestSkillImportWrappedZip defends the archive shape GitHub's "Download ZIP" (and
// `zip -r` of a parent dir) produces: a single wrapper directory is descended into,
// so the skill is found instead of rejected for having no SKILL.md at the top.
func TestSkillImportWrappedZip(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits())
	inner := writeSkillDir(t, "inner", "wrapped", nil)
	outer := filepath.Join(t.TempDir(), "outer")
	if err := os.MkdirAll(filepath.Join(outer, "inner-main"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outer, "inner-main", "SKILL.md"),
		mustReadFile(t, filepath.Join(inner, "SKILL.md")), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Import(zipSkill(t, outer), "tester")
	if err != nil {
		t.Fatalf("Import(wrapped zip): %v", err)
	}
	if got.Name != "inner" || len(got.Files) != 1 {
		t.Fatalf("wrapped zip = %+v, want the inner skill", got)
	}
}

// TestSkillImportURLSource defends the http(s) source: the .zip is fetched, recorded
// as source=url with the URL as its ref, and anything that is not a reachable .zip is
// refused before it can reach the extractor.
func TestSkillImportURLSource(t *testing.T) {
	s, _ := newTestStore(t, DefaultLimits())
	zp := zipSkill(t, writeSkillDir(t, "from-url", "served", nil))
	body := mustReadFile(t, zp)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ".zip") {
			http.Error(w, "not a zip", http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	got, err := s.Import(srv.URL+"/skill.zip", "tester")
	if err != nil {
		t.Fatalf("Import(url): %v", err)
	}
	if got.Name != "from-url" || got.Source != sourceURL || got.SourceRef != srv.URL+"/skill.zip" {
		t.Fatalf("url import = %+v", got)
	}
	if _, err := s.Import(srv.URL+"/skill.tar", "tester"); err == nil {
		t.Fatal("a non-.zip URL was accepted")
	}
	if _, err := s.Import("https://127.0.0.1:1/nope.zip", "tester"); err == nil {
		t.Fatal("an unreachable URL was accepted")
	}
}

// TestSkillGitSpecAndMissingGit defends the git source's spec parsing (the URL, the
// optional #subdir and the name each implies) and the error a machine without git
// gets — a message naming git, not a bare exec failure.
func TestSkillGitSpecAndMissingGit(t *testing.T) {
	if u, sub := splitGitSpec("git+https://host/x/repo.git#skills/foo"); u != "https://host/x/repo.git" || sub != "skills/foo" {
		t.Fatalf("splitGitSpec = %q %q", u, sub)
	}
	if n := defaultNameFor("git+https://host/x/repo.git#skills/foo", sourceGit, ""); n != "foo" {
		t.Fatalf("git subdir name = %q, want foo", n)
	}
	if n := defaultNameFor("git+https://host/x/repo.git", sourceGit, ""); n != "repo" {
		t.Fatalf("git repo name = %q, want repo", n)
	}
	if n := defaultNameFor("https://host/a/b/skill.zip?v=1", sourceURL, ""); n != "skill" {
		t.Fatalf("url name = %q, want skill", n)
	}

	t.Setenv("PATH", t.TempDir())
	s, _ := newTestStore(t, DefaultLimits())
	if _, err := s.Import("git+https://host/x/repo.git", "tester"); err == nil || !strings.Contains(err.Error(), "git is not on PATH") {
		t.Fatalf("Import(git+) without git = %v, want the git-not-on-PATH error", err)
	}
}
