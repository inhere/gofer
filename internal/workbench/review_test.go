package workbench

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestReviewCommentsBecomeNextTurn(t *testing.T) {
	newFixture := func(t *testing.T, session bool, base bool) (*Service, *jobstore.Store, *recordingJobs) {
		t.Helper()
		store := openWorkbenchStore(t)
		resultDir := filepath.Join(t.TempDir(), "result")
		if err := os.MkdirAll(resultDir, 0o755); err != nil {
			t.Fatal(err)
		}
		patch := "diff --git a/a.go b/a.go\n" +
			"index 1111111..2222222 100644\n--- a/a.go\n+++ b/a.go\n" +
			"@@ -1,5 +1,5 @@\n line one\n-line two old\n+line two new\n line three\n line four\n line five\n"
		if err := os.WriteFile(filepath.Join(resultDir, "changes.diff"), []byte(patch), 0o644); err != nil {
			t.Fatal(err)
		}
		rec := jobstore.JobRecord{
			ID: "review-source", ProjectKey: "self", Agent: "cli", Runner: "worker-1",
			Status: job.StatusDone, StartedAt: 10, UpdatedAt: 11, ResultDir: resultDir,
		}
		if session {
			rec.SessionID = "review-session"
		}
		if base {
			rec.BaseSHA = strings.Repeat("a", 40)
		}
		putWorkbenchJob(t, store, rec, "review", "first")
		jobs := &recordingJobs{}
		service := NewService(store, jobs, nil)
		service.now = func() time.Time { return time.Unix(1234, 0) }
		return service, store, jobs
	}

	t.Run("comments_with_context_resume_same_session_and_mark_seen", func(t *testing.T) {
		service, store, jobs := newFixture(t, true, true)
		result, err := service.Review("alice", "s:review-session", ReviewInput{
			Summary: "请修正以下两点。",
			Comments: []ReviewComment{
				{Path: "a.go", Line: 2, Side: ReviewSideNew, Text: "新实现仍需处理边界。"},
				{Path: "a.go", Line: 2, Side: ReviewSideOld, Text: "旧逻辑不能直接删除。"},
			},
		})
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		if result.JobID != "continued-job" || result.ThreadID != "s:review-session" {
			t.Fatalf("result=%+v", result)
		}
		if jobs.jobID != "review-source" || jobs.callerID != "alice" {
			t.Fatalf("resume call=%+v", jobs)
		}
		if !strings.HasPrefix(jobs.prompt, "请修正以下两点。") {
			t.Fatalf("summary is not first:\n%s", jobs.prompt)
		}
		for _, want := range []string{
			"a.go:2 (new)", "+line two new", " line one", " line three", "新实现仍需处理边界。",
			"a.go:2 (old)", "-line two old", "旧逻辑不能直接删除。",
		} {
			if !strings.Contains(jobs.prompt, want) {
				t.Fatalf("prompt missing %q:\n%s", want, jobs.prompt)
			}
		}
		prefs, err := store.ListWorkbenchThreadPrefs("alice")
		if err != nil || len(prefs) != 1 || prefs[0].SeenAt != 1234 {
			t.Fatalf("seen prefs=%+v err=%v", prefs, err)
		}
	})

	t.Run("missing_context_omits_code_block", func(t *testing.T) {
		service, _, jobs := newFixture(t, true, true)
		_, err := service.Review("alice", "s:review-session", ReviewInput{Comments: []ReviewComment{
			{Path: "missing.go", Line: 99, Side: ReviewSideNew, Text: "仍需检查。"},
		}})
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		if strings.Contains(jobs.prompt, "```diff") || !strings.Contains(jobs.prompt, "missing.go:99 (new)") {
			t.Fatalf("unexpected missing-context prompt:\n%s", jobs.prompt)
		}
	})

	t.Run("summary_only_does_not_require_diff_base", func(t *testing.T) {
		service, _, jobs := newFixture(t, true, false)
		_, err := service.Review("alice", "s:review-session", ReviewInput{Summary: "只补充总体意见。"})
		if err != nil || jobs.prompt != "只补充总体意见。" {
			t.Fatalf("summary-only prompt=%q err=%v", jobs.prompt, err)
		}
	})

	t.Run("one_shot_is_not_resumable", func(t *testing.T) {
		service, _, _ := newFixture(t, false, true)
		_, err := service.Review("alice", "j:review-source", ReviewInput{Summary: "continue"})
		if !errors.Is(err, ErrNotResumable) {
			t.Fatalf("err=%v, want ErrNotResumable", err)
		}
	})

	t.Run("limits_and_validation", func(t *testing.T) {
		service, _, _ := newFixture(t, true, true)
		invalid := []struct {
			name  string
			input ReviewInput
		}{
			{name: "empty", input: ReviewInput{}},
			{name: "too_many", input: ReviewInput{Comments: make([]ReviewComment, 51)}},
			{name: "too_long", input: ReviewInput{Comments: []ReviewComment{{Path: "a.go", Line: 1, Side: ReviewSideNew, Text: strings.Repeat("界", 4001)}}}},
			{name: "path", input: ReviewInput{Comments: []ReviewComment{{Line: 1, Side: ReviewSideNew, Text: "x"}}}},
			{name: "line", input: ReviewInput{Comments: []ReviewComment{{Path: "a.go", Side: ReviewSideNew, Text: "x"}}}},
			{name: "side", input: ReviewInput{Comments: []ReviewComment{{Path: "a.go", Line: 1, Side: "middle", Text: "x"}}}},
			{name: "text", input: ReviewInput{Comments: []ReviewComment{{Path: "a.go", Line: 1, Side: ReviewSideNew}}}},
		}
		for _, tc := range invalid {
			t.Run(tc.name, func(t *testing.T) {
				_, err := service.Review("alice", "s:review-session", tc.input)
				if !errors.Is(err, ErrInvalidReview) {
					t.Fatalf("err=%v, want ErrInvalidReview", err)
				}
			})
		}
	})
}
