package job

import (
	"math/rand"
	"reflect"
	"testing"
)

// The cases below are mirrored in web/src/utils/scope.test.ts.
func TestScopeMatch(t *testing.T) {
	cases := []struct {
		glob, file string
		want       bool
	}{
		{"internal/job/**", "internal/job/submit.go", true},
		{"internal/job/**", "internal/job/workflow/engine.go", true},
		{"internal/job/**", "internal/jobstore/todos.go", false},
		{"internal/job/*.go", "internal/job/submit.go", true},
		{"internal/job/*.go", "internal/job/workflow/engine.go", false},
		{"web/src/components/ReviewPanel.vue", "web/src/components/ReviewPanel.vue", true},
		{"internal/job", "internal/job/submit.go", true},
		{"internal/job/", "internal/job/submit.go", true},
		{"./docs/*.md", "docs/a.md", true},
		{"**/*.md", "README.md", true},
		{"**/*.md", "docs/design/x.md", true},
		{"docs/**/x.md", "docs/x.md", true},
		{"src/?.ts", "src/a.ts", true},
		{"src/?.ts", "src/ab.ts", false},
		{"a.b", "aXb", false},
		{"", "anything", false},
	}
	for _, c := range cases {
		if got := ScopeMatch(c.glob, c.file); got != c.want {
			t.Errorf("ScopeMatch(%q, %q) = %v, want %v", c.glob, c.file, got, c.want)
		}
	}
}

func TestDiffFilesAndOutOfScope(t *testing.T) {
	diff := "=== committed (abc..HEAD) ===\n" +
		"diff --git a/internal/job/submit.go b/internal/job/submit.go\n" +
		"index 1..2 100644\n--- a/internal/job/submit.go\n+++ b/internal/job/submit.go\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/old name.go b/new name.go\nsimilarity index 90%\nrename from old name.go\nrename to new name.go\n" +
		"=== uncommitted ===\n" +
		"diff --git a/internal/job/submit.go b/internal/job/submit.go\n" +
		"diff --git a/README.md b/README.md\ndeleted file mode 100644\n"
	files := DiffFiles(diff)
	want := []string{"internal/job/submit.go", "new name.go", "README.md"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("DiffFiles = %q, want %q", files, want)
	}
	if got := OutOfScope(files, nil); got != nil {
		t.Fatalf("no scope = %q, want nil", got)
	}
	if got := OutOfScope(files, []string{" internal/job/** ", "*.md"}); !reflect.DeepEqual(got, []string{"new name.go"}) {
		t.Fatalf("OutOfScope = %q", got)
	}
}

func TestDiffFilesChangedFilesList(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n+x\n\n=== changed files ===\na.go\nb/c.go\n"
	if got := DiffFiles(diff); !reflect.DeepEqual(got, []string{"a.go", "b/c.go"}) {
		t.Fatalf("DiffFiles = %q", got)
	}
}

func TestScopeLockPaths(t *testing.T) {
	cases := []struct {
		name   string
		scope  []string
		want   []string
		reason string
	}{
		{"dir", []string{"internal/job"}, []string{"internal/job"}, ""},
		{"dir globstar", []string{"internal/job/**"}, []string{"internal/job"}, ""},
		{"ext glob", []string{"web/src/*.vue"}, []string{"web/src"}, ""},
		{"partial segment wildcard", []string{"internal/job*"}, []string{"internal"}, ""},
		{"dot slash and trailing slash", []string{"./a/b/"}, []string{"a/b"}, ""},
		{"file itself", []string{"docs/x.md"}, []string{"docs/x.md"}, ""},
		{"dotdot", []string{"a/../b"}, nil, "invalid_glob"},
		{"absolute", []string{"/etc/x"}, nil, "invalid_glob"},
		{"empty prefix globstar", []string{"**/*.go"}, nil, "empty_prefix"},
		{"empty prefix star", []string{"*.md"}, nil, "empty_prefix"},
		{"one empty prefix poisons the set", []string{"src/**", "*.md"}, nil, "empty_prefix"},
		{"contained dropped", []string{"a/x/**", "a/**"}, []string{"a"}, ""},
		{"contained dropped sibling sorts between", []string{"a/x", "a-b", "a"}, []string{"a", "a-b"}, ""},
		{"no common ancestor", []string{"src/api/**", "web/src/*.vue"}, []string{"src/api", "web/src"}, ""},
		{"duplicates and order", []string{"web/**", "src/**", "web"}, []string{"src", "web"}, ""},
		{"empty scope", nil, nil, ""},
		{"blank entries", []string{" ", ""}, nil, ""},
	}
	for _, c := range cases {
		got, reason := ScopeLockPaths(c.scope)
		if reason != c.reason || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: ScopeLockPaths(%q) = %q, %q; want %q, %q", c.name, c.scope, got, reason, c.want, c.reason)
		}
	}
}

// Whatever a glob matches must lie inside the lock prefix derived from it, otherwise a
// job could write a covered file without holding the lock.
func TestScopeLockPathsCoversScopeMatch(t *testing.T) {
	globs := []string{
		"internal/job/**", "internal/job/*.go", "internal/job", "internal/job*", "./docs/*.md",
		"docs/**/x.md", "src/?.ts", "src/a?/**", "web/src/*.vue", "a.b", "a/b/c.go", "**/*.md", "*.md",
	}
	segs := []string{"internal", "internal/job", "internal/jobx", "internal/jobstore", "docs", "docs/design", "src", "src/ab", "src/a", "web/src", "web", "a", "a/b"}
	names := []string{"x.md", "a.go", "a.ts", "ab.ts", "x.vue", "a.b", "aXb", "c.go", "submit.go", "x.go"}
	rng := rand.New(rand.NewSource(341))
	files := []string{"internal/jobx/a.go", "internal/job/x.go", "README.md"}
	for i := 0; i < 400; i++ {
		files = append(files, segs[rng.Intn(len(segs))]+"/"+names[rng.Intn(len(names))])
		files = append(files, segs[rng.Intn(len(segs))]+"/"+segs[rng.Intn(len(segs))]+"/"+names[rng.Intn(len(names))])
	}
	files = append(files, names...)
	for _, g := range globs {
		prefixes, reason := ScopeLockPaths([]string{g})
		if reason != "" {
			continue // no fixed prefix: the caller falls back to locking the whole cwd
		}
		for _, f := range files {
			if !ScopeMatch(g, f) {
				continue
			}
			inside := false
			for _, p := range prefixes {
				inside = inside || dirContains(p, f)
			}
			if !inside {
				t.Errorf("glob %q matches %q but prefixes %q do not contain it", g, f, prefixes)
			}
		}
	}
}
