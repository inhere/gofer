package job

import (
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
