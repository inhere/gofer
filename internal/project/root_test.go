package project

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// rootFixture lays out <top>/proj (the root), <top>/outside (a sibling dir the
// root must never touch) and, inside proj, in/real.txt.
type rootFixture struct {
	root, outside string
}

func newRootFixture(t *testing.T) rootFixture {
	t.Helper()
	top := t.TempDir()
	f := rootFixture{root: filepath.Join(top, "proj"), outside: filepath.Join(top, "outside")}
	for _, d := range []string{filepath.Join(f.root, "in"), f.outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.root, "in", "real.txt"), []byte("INSIDE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.outside, "secret.txt"), []byte("LEAK"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func readAll(t *testing.T, f *os.File) string {
	t.Helper()
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func dirEntries(t *testing.T, dir string) int {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(es)
}

func TestOpenInRoot_Normal(t *testing.T) {
	fx := newRootFixture(t)
	f, err := OpenInRoot(fx.root, "in/real.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, f); got != "INSIDE" {
		t.Fatalf("got %q", got)
	}
}

func TestOpenInRoot_Missing(t *testing.T) {
	fx := newRootFixture(t)
	_, err := OpenInRoot(fx.root, "in/none.txt")
	if !os.IsNotExist(err) {
		t.Fatalf("err=%v, want not-exist", err)
	}
}

func TestOpenInRoot_FileLinkOutside(t *testing.T) {
	fx := newRootFixture(t)
	mustSymlink(t, filepath.Join("..", "outside", "secret.txt"), filepath.Join(fx.root, "leak.txt"))
	f, err := OpenInRoot(fx.root, "leak.txt")
	if err == nil {
		_ = f.Close()
		t.Fatal("link to a file outside the root must not open")
	}
	if os.IsNotExist(err) {
		t.Fatalf("escape must not look like not-exist: %v", err)
	}
}

func TestOpenInRoot_DirLinkOutside(t *testing.T) {
	fx := newRootFixture(t)
	mustSymlink(t, filepath.Join("..", "outside"), filepath.Join(fx.root, "out"))
	f, err := OpenInRoot(fx.root, "out/secret.txt")
	if err == nil {
		_ = f.Close()
		t.Fatal("intermediate dir link outside the root must not open")
	}
}

func TestOpenInRoot_RelativeLinkInside(t *testing.T) {
	fx := newRootFixture(t)
	mustSymlink(t, "in", filepath.Join(fx.root, "ok"))
	f, err := OpenInRoot(fx.root, "ok/real.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, f); got != "INSIDE" {
		t.Fatalf("got %q", got)
	}
}

func TestPlaceInRoot_CommitCreatesParents(t *testing.T) {
	fx := newRootFixture(t)
	p, err := PlaceInRoot(fx.root, "a/b/new.txt", "x1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fx.root, "a", "b", "new.txt.gofer-tmp-x1")); err != nil {
		t.Fatalf("temp file name: %v", err)
	}
	if _, err := p.File().WriteString("hello"); err != nil {
		t.Fatal(err)
	}
	if err := p.Commit(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(fx.root, "a", "b", "new.txt"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("dest=%q err=%v", b, err)
	}
	if n := dirEntries(t, filepath.Join(fx.root, "a", "b")); n != 1 {
		t.Fatalf("leftover temp file, %d entries", n)
	}
	p.Abort() // after Commit: no-op
	if _, err := os.Stat(filepath.Join(fx.root, "a", "b", "new.txt")); err != nil {
		t.Fatalf("Abort after Commit removed the destination: %v", err)
	}
}

func TestPlaceInRoot_AbortRemovesTemp(t *testing.T) {
	fx := newRootFixture(t)
	p, err := PlaceInRoot(fx.root, "in/new.txt", "x2")
	if err != nil {
		t.Fatal(err)
	}
	p.Abort()
	p.Abort() // repeatable
	if _, err := os.Lstat(filepath.Join(fx.root, "in", "new.txt.gofer-tmp-x2")); !os.IsNotExist(err) {
		t.Fatalf("temp file left: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(fx.root, "in", "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("destination must not exist: %v", err)
	}
}

func TestPlaceInRoot_CommitOverwritesExisting(t *testing.T) {
	fx := newRootFixture(t)
	p, err := PlaceInRoot(fx.root, "in/real.txt", "x3")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = p.File().WriteString("NEW")
	if err := p.Commit(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(fx.root, "in", "real.txt"))
	if string(b) != "NEW" {
		t.Fatalf("got %q", b)
	}
}

func TestPlaceInRoot_CommitReplacesLinkItself(t *testing.T) {
	fx := newRootFixture(t)
	target := filepath.Join(fx.outside, "secret.txt")
	mustSymlink(t, filepath.Join("..", "outside", "secret.txt"), filepath.Join(fx.root, "lnk.txt"))
	p, err := PlaceInRoot(fx.root, "lnk.txt", "x4")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = p.File().WriteString("NEW")
	if err := p.Commit(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "LEAK" {
		t.Fatalf("the link target outside was modified: %q", b)
	}
	if fi, err := os.Lstat(filepath.Join(fx.root, "lnk.txt")); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("link should be replaced by a regular file: %v %v", fi, err)
	}
}

func TestPlaceInRoot_DirLinkOutsideFails(t *testing.T) {
	fx := newRootFixture(t)
	mustSymlink(t, filepath.Join("..", "outside"), filepath.Join(fx.root, "out"))
	before := dirEntries(t, fx.outside)
	for _, rel := range []string{"out/x.txt", "out/sub/x.txt"} {
		p, err := PlaceInRoot(fx.root, rel, "x5")
		if err == nil {
			p.Abort()
			t.Fatalf("%s: placement through an escaping dir link must fail", rel)
		}
	}
	if after := dirEntries(t, fx.outside); after != before {
		t.Fatalf("outside dir changed: %d -> %d entries", before, after)
	}
}

func TestExistsInRoot(t *testing.T) {
	fx := newRootFixture(t)
	mustSymlink(t, "no-such-target", filepath.Join(fx.root, "dangling"))
	mustSymlink(t, filepath.Join("..", "outside"), filepath.Join(fx.root, "out"))

	for _, c := range []struct {
		rel  string
		want bool
	}{
		{"in/real.txt", true},
		{"in/none.txt", false},
		{"none/deeper/x.txt", false},
		{"dangling", true},
		{"out", true}, // a link out of the root, judged as the link itself
	} {
		got, err := ExistsInRoot(fx.root, c.rel)
		if err != nil || got != c.want {
			t.Fatalf("%s: got %v, %v; want %v", c.rel, got, err, c.want)
		}
	}

	if _, err := ExistsInRoot(fx.root, "out/secret.txt"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("intermediate dir escaping the root must be an error, got %v", err)
	}
}
