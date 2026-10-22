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

func TestDeletionHotspotsCarriesNetAndHeavyFlag(t *testing.T) {
	var b strings.Builder
	// A pure deletion: 30 lost, 0 gained → heavy, net -30.
	b.WriteString(diffFor("gone/old.go", 30, 0))
	// A rewrite that added as much as it removed: not heavy, net 0.
	b.WriteString(diffFor("even/rewrite.go", 20, 20))
	// Mostly additions: not heavy, net positive.
	b.WriteString(diffFor("big/new.go", 12, 40))

	got := BuildReviewHints(b.String(), "").DeletionHotspots
	if len(got) != 3 {
		t.Fatalf("want 3 hotspots, got %d: %+v", len(got), got)
	}
	if got[0].Path != "gone/old.go" || got[0].Net != -30 || !got[0].DeletionHeavy {
		t.Fatalf("pure deletion is heavy with net -30: %+v", got[0])
	}
	if got[1].Path != "even/rewrite.go" || got[1].Net != 0 || got[1].DeletionHeavy {
		t.Fatalf("balanced rewrite is not heavy: %+v", got[1])
	}
	if got[2].Path != "big/new.go" || got[2].Net != 28 || got[2].DeletionHeavy {
		t.Fatalf("addition-heavy file is not heavy: %+v", got[2])
	}
}

// diffFor builds one file section of a diff with the given added/deleted body lines.
func diffFor(path string, deleted, added int) string {
	var b strings.Builder
	b.WriteString("diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path + "\n")
	b.WriteString("@@ -1,1 +1,1 @@\n")
	for i := 0; i < deleted; i++ {
		b.WriteString("-old line\n")
	}
	for i := 0; i < added; i++ {
		b.WriteString("+new line\n")
	}
	return b.String()
}

func TestDeletionHotspotsRenameUsesNewPathAndSkipsBinary(t *testing.T) {
	// A rename with heavy editing: git prints the old and the new path in the header
	// and "rename from/to" lines; the hotspot must name the new path.
	diff := `diff --git a/pkg/old_name.go b/pkg/new_name.go
similarity index 40%
rename from pkg/old_name.go
rename to pkg/new_name.go
--- a/pkg/old_name.go
+++ b/pkg/new_name.go
@@ -1,12 +1,2 @@
` + strings.Repeat("-old body\n", 12) + strings.Repeat("+new body\n", 2) +
		// A binary file: "Binary files … differ" plus (with --binary) base85 data lines
		// that start with +/-, none of which is churn.
		`diff --git a/assets/logo.png b/assets/logo.png
index 1111111..2222222 100644
Binary files a/assets/logo.png and b/assets/logo.png differ
` +
		`diff --git a/assets/blob.bin b/assets/blob.bin
index 3333333..4444444 100644
GIT binary patch
literal 12
` + strings.Repeat("-fake base85 body line\n", 40)

	got := BuildReviewHints(diff, "").DeletionHotspots
	if len(got) != 1 {
		t.Fatalf("only the renamed text file is a hotspot, got %d: %+v", len(got), got)
	}
	if got[0].Path != "pkg/new_name.go" || got[0].Added != 2 || got[0].Deleted != 12 {
		t.Fatalf("rename keeps the new path: %+v", got[0])
	}
	if !got[0].DeletionHeavy {
		t.Fatalf("12 deleted vs 2 added is heavy: %+v", got[0])
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

func TestReportVerifyExtractsBlocksUnderVerifyHeading(t *testing.T) {
	report := `## 验收标准
- [ ] 测试通过

` + "```bash" + `
not a verification block: no verify heading above
` + "```" + `

## 验证

` + "```bash" + `
go test ./internal/job/
ok  github.com/owner/repo/internal/job
` + "```" + `

The step above ran in the repo root.

### Verification details
` + "```" + `
tsc=0
vitest: 12 passed
` + "```" + `

## 发现但不碰
` + "```" + `
job.go:12: stale comment
` + "```" + `
`

	got := BuildReviewHints("", report).ReportVerify
	if len(got) != 2 {
		t.Fatalf("want 2 report verify blocks, got %d: %+v", len(got), got)
	}
	if got[0].Heading != "验证" || got[0].Source != VerifySourceAgent {
		t.Fatalf("first block keeps its heading and agent source: %+v", got[0])
	}
	if !strings.Contains(got[0].Text, "ok  github.com/owner/repo/internal/job") {
		t.Fatalf("first block body: %q", got[0].Text)
	}
	// The non-verify code block and the 发现 block must not be reported as verification.
	for _, b := range got {
		if strings.Contains(b.Text, "not a verification block") || strings.Contains(b.Text, "stale comment") {
			t.Fatalf("only verify-section blocks belong here: %+v", b)
		}
	}
	if got[1].Heading != "Verification details" || !strings.Contains(got[1].Text, "vitest: 12 passed") {
		t.Fatalf("second block: %+v", got[1])
	}
}

func TestReportVerifyCapsBlocksAndKeepsTail(t *testing.T) {
	var b strings.Builder
	for i := 0; i < reviewReportVerifyLimit+2; i++ {
		b.WriteString("## 验证 " + string(rune('a'+i)) + "\n")
		b.WriteString("```\n")
		for j := 0; j < reviewReportVerifyLineLimit+3; j++ {
			b.WriteString("line " + string(rune('a'+j)) + "\n")
		}
		b.WriteString("verdict\n```\n")
	}
	got := BuildReviewHints("", b.String()).ReportVerify
	if len(got) != reviewReportVerifyLimit {
		t.Fatalf("cap at %d blocks, got %d", reviewReportVerifyLimit, len(got))
	}
	first := got[0].Text
	if !strings.Contains(first, "前面 4 行已省略") || !strings.HasSuffix(first, "verdict") {
		t.Fatalf("long block keeps its tail behind a marker: %q", first)
	}
}

func TestReportVerifyUnterminatedFenceStillYieldsBlock(t *testing.T) {
	report := "## 验证\n```\ngo vet ./...\nvet=0\n"
	got := BuildReviewHints("", report).ReportVerify
	if len(got) != 1 || !strings.Contains(got[0].Text, "vet=0") {
		t.Fatalf("a block running to the end of the report is taken: %+v", got)
	}
}

func TestBuildReviewHintsEmptyInputs(t *testing.T) {
	got := BuildReviewHints("", "")
	if len(got.DeletionHotspots) != 0 || len(got.RemovedComments) != 0 || len(got.VerifyLines) != 0 ||
		len(got.ReportVerify) != 0 {
		t.Fatalf("no diff and no report must yield no hints: %+v", got)
	}
	// A diff truncated at the size cap ends with a name list, not a patch: no churn.
	truncated := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1,1 +1,1 @@\n-a\n+b\n\n=== changed files ===\nx.go\ny.go\n"
	if got := BuildReviewHints(truncated, ""); len(got.DeletionHotspots) != 0 {
		t.Fatalf("changed-files list is not churn: %+v", got.DeletionHotspots)
	}
}
