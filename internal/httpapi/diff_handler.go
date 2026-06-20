package httpapi

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/gookit/rux/v2"
)

// handleGetDiff serves a job's E12 git-diff capture (job-outcomes-audit, P3).
//
//   - default: returns the persisted `git diff --stat` summary as
//     {"summary": "..."} (an unknown job is 404; a job with no diff returns an
//     empty summary, not an error).
//   - ?full=1: streams <result_dir>/changes.diff (the full uncommitted-changes
//     patch) as text/plain. A job that captured no diff (file absent) is 404.
//
// The summary is "未提交改动 / uncommitted changes" (tracked vs HEAD/index, D4) —
// untracked files and agent self-commits are out of scope for v1.
func (s *Server) handleGetDiff(c *rux.Context) {
	id := c.Param("id")
	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}

	if c.Query("full") == "1" {
		p := filepath.Join(res.ResultDir, "changes.diff")
		if fi, err := os.Stat(p); err != nil || fi.IsDir() {
			writeError(c, http.StatusNotFound, "no diff", "this job captured no full diff")
			return
		}
		c.SetHeader("Content-Type", "text/plain; charset=utf-8")
		http.ServeFile(c.Resp, c.Req, p)
		return
	}

	c.JSON(http.StatusOK, map[string]string{"summary": res.DiffSummary})
}
