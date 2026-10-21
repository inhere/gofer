package job

import (
	"strings"
	"testing"
)

// sampleDiff is a two-file unified diff shaped like a captured changes.diff: a
// worktree's committed section, a comment that only moved, a comment that was
// dropped, and a block comment continuation line.
const sampleDiff = `=== committed (base..HEAD) ===
diff --git a/internal/job/hot.go b/internal/job/hot.go
--- a/internal/job/hot.go
+++ b/internal/job/hot.go
@@ -10,4 +10,5 @@ func Hot() {
-// Old doc comment.
+// Old doc comment.
+func added() {}
-	obsolete := 1
-	obsolete2 := 2
-	obsolete3 := 3
+	return nil
diff --git a/web/src/app.ts b/web/src/app.ts
--- a/web/src/app.ts
+++ b/web/src/app.ts
@@ -1,3 +1,3 @@
-// Dropped note.
+const x = 1
-/* block start */
+const y = 2
- * block body
+const z = 3
`

func TestDeletionHotspotsThresholdSortAndLimit(t *testing.T) {
	var b strings.Builder
	// Two files over the threshold, ordered by deleted lines; one below it.
	for i, name := range []string{"a/few.go", "b/many.go", "c/most.go"} {
		deleted := 3
		if i == 1 {
			deleted = 10
		}
		if i == 2 {
			deleted = 12
		}
		b.WriteString("diff --git a/" + name + " b/" + name + "\n")
		b.WriteString("--- a/" + name + "\n+++ b/" + name + "\n")
		b.WriteString("@@ -1,1 +1,1 @@\n")
		for j := 0; j < deleted; j++ {
			b.WriteString("-line " + name + "\n")
		}
		b.WriteString("+added\n")
	}
	got := BuildReviewHints(b.String(), "").DeletionHotspots
	if len(got) != 2 {
		t.Fatalf("want 2 hotspots (deleted >= 10), got %d: %+v", len(got), got)
	}
	if got[0].Path != "c/most.go" || got[0].Deleted != 12 {
		t.Fatalf("worst file first: %+v", got)
	}
	if got[1].Path != "b/many.go" || got[1].Added != 1 {
		t.Fatalf("second hotspot: %+v", got)
	}

	var many strings.Builder
	for i := 0; i < reviewHotspotLimit+3; i++ {
		name := "f" + string(rune('a'+i)) + ".go"
		many.WriteString("diff --git a/" + name + " b/" + name + "\n@@ -1,1 +1,1 @@\n")
		for j := 0; j < 10+i; j++ {
			many.WriteString("-x\n")
		}
	}
	if got := BuildReviewHints(many.String(), "").DeletionHotspots; len(got) != reviewHotspotLimit {
		t.Fatalf("cap at %d hotspots, got %d", reviewHotspotLimit, len(got))
	}
}

func TestRemovedCommentsSkipMovedOnesAndReportOldLine(t *testing.T) {
	got := BuildReviewHints(sampleDiff, "").RemovedComments
	if len(got) != 3 {
		t.Fatalf("want 3 removed comments, got %d: %+v", len(got), got)
	}
	// "// Old doc comment." is added again in the same file: it moved, it was not deleted.
	for _, rc := range got {
		if strings.Contains(rc.Text, "Old doc comment") {
			t.Fatalf("a moved comment must not count as removed: %+v", got)
		}
	}
	if got[0].Path != "web/src/app.ts" || got[0].Text != "// Dropped note." {
		t.Fatalf("first removed comment: %+v", got[0])
	}
	if got[0].Line != 1 {
		t.Fatalf("old-file line number: got %d want 1", got[0].Line)
	}
	if got[1].Text != "/* block start */" || got[2].Text != "* block body" {
		t.Fatalf("block comment lines: %+v", got)
	}
}

func TestRemovedCommentsCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/x.go b/x.go\n@@ -1,1 +1,1 @@\n")
	for i := 0; i < reviewRemovedCommentLimit+5; i++ {
		b.WriteString("-// note\n")
	}
	if got := BuildReviewHints(b.String(), "").RemovedComments; len(got) != reviewRemovedCommentLimit {
		t.Fatalf("cap at %d removed comments, got %d", reviewRemovedCommentLimit, len(got))
	}
}

func TestVerifyLinesShapesOrderAndCap(t *testing.T) {
	report := `## 验证
ok  	github.com/inhere/gofer/internal/job	1.2s
FAIL	github.com/inhere/gofer/internal/web	0.3s
--- FAIL: TestThing (0.00s)
PASS
SUGA_EXIT=0
exit=1
 Test Files  12 passed (12)
     Tests  345 passed (345)
tsc=0
no verification result here
ok  	github.com/inhere/gofer/internal/job	1.2s
`
	got := verifyLines(report)
	want := []string{
		"ok  \tgithub.com/inhere/gofer/internal/job\t1.2s",
		"FAIL\tgithub.com/inhere/gofer/internal/web\t0.3s",
		"--- FAIL: TestThing (0.00s)",
		"PASS",
		"SUGA_EXIT=0",
		"exit=1",
		"Test Files  12 passed (12)",
		"Tests  345 passed (345)",
		"tsc=0",
	}
	if len(got) != len(want) {
		t.Fatalf("verify lines: got %d %q", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d: got %q want %q", i, got[i], want[i])
		}
	}

	var many strings.Builder
	for i := 0; i < reviewVerifyLineLimit+5; i++ {
		many.WriteString("ok  \tpkg/" + string(rune('a'+i)) + "\n")
	}
	if got := verifyLines(many.String()); len(got) != reviewVerifyLineLimit {
		t.Fatalf("cap at %d verify lines, got %d", reviewVerifyLineLimit, len(got))
	}
}

func TestBuildReviewHintsEmptyInputs(t *testing.T) {
	got := BuildReviewHints("", "")
	if len(got.DeletionHotspots) != 0 || len(got.RemovedComments) != 0 || len(got.VerifyLines) != 0 {
		t.Fatalf("no diff and no report must yield no hints: %+v", got)
	}
	// A diff truncated at the size cap ends with a name list, not a patch: no churn.
	truncated := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1,1 +1,1 @@\n-a\n+b\n\n=== changed files ===\nx.go\ny.go\n"
	if got := BuildReviewHints(truncated, ""); len(got.DeletionHotspots) != 0 {
		t.Fatalf("changed-files list is not churn: %+v", got.DeletionHotspots)
	}
}
