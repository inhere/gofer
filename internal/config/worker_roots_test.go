package config

import (
	"os"
	"path/filepath"
	"testing"
)

func rootsOf(pairs ...[2]string) *WorkerConfig {
	wc := &WorkerConfig{}
	for _, p := range pairs {
		wc.Roots = append(wc.Roots, WorkerRoot{From: p[0], To: p[1]})
	}
	return wc
}

// TestMapRoot covers the core mapping semantics (P3 T2-B): longest boundary-
// aligned prefix wins, overlapping-root exceptions including sub-paths (H3
// first-class scenario), non-aligned boundaries miss, Windows drive + case,
// trailing slashes, and misses returning ("", false) — never an empty "success".
//
// MapRoot is a LOGICAL path resolver: this table must also pass under
// GOOS=linux. Synthetic (non-existent) To roots keep it independent of the host
// filesystem.
func TestMapRoot(t *testing.T) {
	// Overlap: a wildcard root plus a longer, more-specific exception root.
	overlap := rootsOf(
		[2]string{"D:/work/x", "/host/work/x"},
		[2]string{"D:/work/x/proj-a", "/host/work/x/proj-b"},
	)
	multi := rootsOf(
		[2]string{"/srv/a", "/host/a"},
		[2]string{"/srv/a/b", "/host/deep/b"},
	)

	tests := []struct {
		name     string
		wc       *WorkerConfig
		logical  string
		wantHost string
		wantOK   bool
	}{
		{"multi longest wins", multi, "/srv/a/b/c", "/host/deep/b/c", true},
		{"multi shorter root", multi, "/srv/a/x", "/host/a/x", true},
		{"exact root maps to To", multi, "/srv/a", "/host/a", true},

		// H3 first-class: overlap exception must apply to sub-paths too.
		{"overlap exception root", overlap, "D:/work/x/proj-a", "/host/work/x/proj-b", true},
		{"overlap exception subpath", overlap, "D:/work/x/proj-a/sub", "/host/work/x/proj-b/sub", true},
		{"overlap wildcard sibling", overlap, "D:/work/x/proj-c/sub", "/host/work/x/proj-c/sub", true},

		// Boundary alignment: /a/b must not swallow /a/bc.
		{"boundary not aligned", rootsOf([2]string{"/a/b", "/host/b"}), "/a/bc", "", false},
		{"boundary aligned child", rootsOf([2]string{"/a/b", "/host/b"}), "/a/b/c", "/host/b/c", true},

		// Windows drive + case-insensitive matching.
		{"windows lower logical", rootsOf([2]string{"D:/work", "/host/work"}), "d:/work/proj", "/host/work/proj", true},
		{"windows upper logical", rootsOf([2]string{"d:/work", "/host/work"}), "D:/WORK/proj", "/host/work/proj", true},
		{"windows backslashes", rootsOf([2]string{"D:/work", "/host/work"}), `D:\work\proj\sub`, "/host/work/proj/sub", true},

		// POSIX is case-sensitive: /Srv != /srv.
		{"posix case sensitive miss", rootsOf([2]string{"/srv", "/host"}), "/Srv/x", "", false},

		// Trailing slashes on both sides normalize away.
		{"trailing slash logical", rootsOf([2]string{"/srv/a", "/host/a"}), "/srv/a/b/", "/host/a/b", true},
		{"trailing slash from/to", rootsOf([2]string{"/srv/a/", "/host/a/"}), "/srv/a/b", "/host/a/b", true},

		// Misses.
		{"no root matches", multi, "/other/path", "", false},
		{"empty roots", &WorkerConfig{}, "/srv/a", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			host, ok := tc.wc.MapRoot(tc.logical)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (host=%q)", ok, tc.wantOK, host)
			}
			if host != tc.wantHost {
				t.Fatalf("host = %q, want %q", host, tc.wantHost)
			}
			// Invariant: a miss must never leak a non-empty host.
			if !ok && host != "" {
				t.Fatalf("miss returned non-empty host %q", host)
			}
		})
	}
}

// TestMapRootContainment is the acceptance-23 reverse table (lexical): a logical
// path escaping its From via "..", and a To carrying a ".." segment, both yield
// ("", false). These two are the falsification targets — reverting the
// containment block to "join-then-Clean" must make them red.
func TestMapRootContainment(t *testing.T) {
	tests := []struct {
		name    string
		wc      *WorkerConfig
		logical string
	}{
		{"logical dotdot escapes From", rootsOf([2]string{"/root", "/host/root"}), "/root/../outside"},
		{"logical dotdot deep escape", rootsOf([2]string{"/root/a", "/host/root/a"}), "/root/a/../../etc"},
		{"To contains dotdot segment", rootsOf([2]string{"/root", "/host/../evil"}), "/root/sub"},
		{"From contains dotdot segment", rootsOf([2]string{"/root/../x", "/host/x"}), "/root/../x/sub"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			host, ok := tc.wc.MapRoot(tc.logical)
			if ok {
				t.Fatalf("expected containment rejection, got host=%q ok=true", host)
			}
			if host != "" {
				t.Fatalf("rejection returned non-empty host %q", host)
			}
		})
	}
}

// TestMapRootSymlinkEscape proves the symlink half of B6 with real symlinks: a
// symlink inside To pointing OUT of To yields ("", false); a normal symlink
// staying inside To still maps and the host stays under the real To.
func TestMapRootSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	to := filepath.Join(base, "to")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{to, outside, filepath.Join(to, "inbounds")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// escape: <to>/link -> <outside>
	if err := os.Symlink(outside, filepath.Join(to, "link")); err != nil {
		t.Fatalf("symlink escape: %v", err)
	}
	// safe: <to>/safe -> <to>/inbounds
	if err := os.Symlink(filepath.Join(to, "inbounds"), filepath.Join(to, "safe")); err != nil {
		t.Fatalf("symlink safe: %v", err)
	}

	wc := rootsOf([2]string{"/srv", to})

	if host, ok := wc.MapRoot("/srv/link/secret"); ok {
		t.Fatalf("symlink escape should reject, got host=%q", host)
	}

	host, ok := wc.MapRoot("/srv/safe/file")
	if !ok {
		t.Fatal("in-bounds symlink should map, got ok=false")
	}
	realTo, err := filepath.EvalSymlinks(to)
	if err != nil {
		t.Fatalf("evalsymlinks to: %v", err)
	}
	if escapesDir(realTo, realPathBestEffort(host)) {
		t.Fatalf("mapped host %q escaped real To %q", host, realTo)
	}
}
