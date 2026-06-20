package httpapi

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSafeJoinUnderLegal asserts plain and subdir names join cleanly under base.
func TestSafeJoinUnderLegal(t *testing.T) {
	base := t.TempDir()
	cases := []string{"a.txt", "sub/b", "sub/deep/c.bin", "./d", "x/../y"}
	for _, name := range cases {
		full, err := safeJoinUnder(base, name)
		if err != nil {
			t.Fatalf("safeJoinUnder(%q) unexpected error: %v", name, err)
		}
		// Result must stay inside base.
		rel, relErr := filepath.Rel(base, full)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("safeJoinUnder(%q) escaped base: full=%q rel=%q", name, full, rel)
		}
	}
}

// TestSafeJoinUnderRejected asserts traversal, absolute and empty names are
// rejected.
func TestSafeJoinUnderRejected(t *testing.T) {
	base := t.TempDir()
	bad := []string{
		"",                 // empty
		"../x",             // parent escape
		"../../etc/passwd", // deep parent escape
		"/etc/passwd",      // absolute
		"..",               // bare parent
		"sub/../../x",      // climbs out via subdir
	}
	for _, name := range bad {
		if _, err := safeJoinUnder(base, name); err == nil {
			t.Fatalf("safeJoinUnder(%q) should be rejected, got nil error", name)
		}
	}
}

// TestSafeJoinUnderSymlinkEscape plants a symlink inside base pointing outside
// it; resolving through it must be rejected (symlink escape, design §9).
func TestSafeJoinUnderSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on windows")
	}
	root := t.TempDir()
	base := filepath.Join(root, "artifacts")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("S"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	// base/evil -> ../outside  (a symlink to a sibling dir of base).
	link := filepath.Join(base, "evil")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	// Lexically "evil/secret.txt" stays under base, but the real target escapes.
	if _, err := safeJoinUnder(base, "evil/secret.txt"); err == nil {
		t.Fatalf("safeJoinUnder must reject symlink escape evil/secret.txt")
	}
}

// TestSafeJoinUnderSymlinkInternal asserts a symlink that stays inside base is
// allowed (only escapes are rejected).
func TestSafeJoinUnderSymlinkInternal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on windows")
	}
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	if err := os.WriteFile(filepath.Join(real, "f.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatalf("write f: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := safeJoinUnder(base, "link/f.txt"); err != nil {
		t.Fatalf("internal symlink should be allowed, got %v", err)
	}
}
