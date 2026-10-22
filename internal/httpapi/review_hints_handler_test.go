package httpapi

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/store"
)

// writeStdout drops an agent report into a job's stdout log (what the review screen
// reads as 汇报), replacing whatever the test job wrote.
func writeStdout(t *testing.T, s *Server, id, report string) {
	t.Helper()
	res, ok := s.jobs.Get(id)
	if !ok {
		t.Fatalf("job %s not found", id)
	}
	fs := store.NewFileStore(filepath.Dir(res.ResultDir))
	w, err := fs.LogWriter(id, store.StreamStdout)
	if err != nil {
		t.Fatalf("open stdout log: %v", err)
	}
	if _, err := w.Write([]byte(report)); err != nil {
		t.Fatalf("write stdout log: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close stdout log: %v", err)
	}
}

// TestGetReviewHintsServesDiffAndReportHints asserts the endpoint pairs the diff's
// deletion hotspots (net change, deletion-heavy flag, rename under the new path,
// binary excluded) with the verification blocks of the agent's report, the latter
// marked as the agent's own account.
func TestGetReviewHintsServesDiffAndReportHints(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	id := runDoneJob(t, s)

	var diff strings.Builder
	diff.WriteString("diff --git a/old.go b/new.go\nsimilarity index 40%\nrename from old.go\nrename to new.go\n")
	diff.WriteString("--- a/old.go\n+++ b/new.go\n@@ -1,12 +1,2 @@\n")
	diff.WriteString(strings.Repeat("-	obsolete := 1\n", 12))
	diff.WriteString("+func kept() {}\n+var moved = 1\n")
	diff.WriteString("diff --git a/logo.png b/logo.png\nBinary files a/logo.png and b/logo.png differ\n")
	writeDiff(t, s, id, diff.String())
	writeStdout(t, s, id, "## 验证\n\n```\ngo test ./...\nok  example.com/pkg\n```\n")

	resp := do(t, s, http.MethodGet, "/v1/jobs/"+id+"/review-hints", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("review hints status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		ID    string          `json:"id"`
		Hints job.ReviewHints `json:"review_hints"`
	}
	decode(t, resp, &body)
	if body.ID != id {
		t.Fatalf("body id=%q want %q", body.ID, id)
	}
	if len(body.Hints.DeletionHotspots) != 1 {
		t.Fatalf("want 1 hotspot (binary skipped), got %+v", body.Hints.DeletionHotspots)
	}
	hot := body.Hints.DeletionHotspots[0]
	if hot.Path != "new.go" || hot.Added != 2 || hot.Deleted != 12 || hot.Net != -10 || !hot.DeletionHeavy {
		t.Fatalf("hotspot: %+v", hot)
	}
	if len(body.Hints.ReportVerify) != 1 || body.Hints.ReportVerify[0].Source != job.VerifySourceAgent ||
		!strings.Contains(body.Hints.ReportVerify[0].Text, "ok  example.com/pkg") {
		t.Fatalf("report verify: %+v", body.Hints.ReportVerify)
	}
}
