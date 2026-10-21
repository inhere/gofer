package httpapi

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/store"
)

// reviewHintsLogBytes is the stdout window the review hints read the agent's report
// from — the same 64KB `job review` and the web report tab already fetch, so the
// hints describe exactly the text the supervisor sees.
const reviewHintsLogBytes = 64 * 1024

// handleGetReviewHints serves a job's review hints (gofer-hjj1): deletion hotspots,
// removed comment lines and the report's verification result lines, computed from
// the same two inputs the review screen shows (the captured changes.diff and the
// stdout tail). Computed on demand rather than stored, so the hints stay in step
// with the diff a reviewer is looking at.
//
// A job that captured no diff or wrote no report is 200 with empty sections: the
// hints are best-effort, and "nothing suspicious found" is not an error. Only an
// unknown job / a caller who may not read it is refused.
func (s *Server) handleGetReviewHints(c *rux.Context) {
	id := c.Param("id")
	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}
	if !s.mayReadJob(c, res) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return
	}

	var diff string
	if res.ResultDir != "" {
		if b, err := os.ReadFile(filepath.Join(res.ResultDir, "changes.diff")); err == nil {
			diff = string(b)
		}
	}
	var report string
	if res.ResultDir != "" {
		fs := store.NewFileStore(filepath.Dir(res.ResultDir))
		if b, err := fs.ReadLogTail(id, store.StreamStdout, reviewHintsLogBytes); err == nil {
			report = string(b)
		}
	}

	c.JSON(http.StatusOK, map[string]any{
		"id":           res.ID,
		"review_hints": job.BuildReviewHints(diff, report),
	})
}
