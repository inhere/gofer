package commands

import "testing"

func TestDedupeDirsSeparatorsAndCase(t *testing.T) {
	in := []string{`D:/work/proj`, `D:\work\proj`, `d:\Work\Proj\`, `D:/work/other`}
	// Windows: case-insensitive, separators unified.
	got := dedupeDirs(in, true)
	if len(got) != 2 {
		t.Fatalf("windows dedupe = %v, want 2 entries", got)
	}
	// Case-sensitive filesystem: separators still unified, case kept distinct.
	got = dedupeDirs(in, false)
	if len(got) != 3 {
		t.Fatalf("case-sensitive dedupe = %v, want 3 entries", got)
	}
	if dirKey(`D:\a\b\..\c`, false) != "D:/a/c" {
		t.Fatalf("dirKey did not clean: %q", dirKey(`D:\a\b\..\c`, false))
	}
}
