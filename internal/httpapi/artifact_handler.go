package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
)

// handleListArtifacts returns a job's artifact manifest ([{name,size,mtime}]).
// It prefers the persisted manifest (job.ArtifactsJSON, captured at finish);
// when that is empty it falls back to a live scan of <result_dir>/artifacts/
// so a job whose capture was skipped (or pre-dates capture) still lists files
// present on disk. An unknown id is a 404. The array is always non-nil, so an
// empty result serialises as {"artifacts":[]}.
func (s *Server) handleListArtifacts(c *rux.Context) {
	id := c.Param("id")
	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}

	items := manifestFor(res)
	c.JSON(http.StatusOK, map[string]any{"artifacts": items})
}

// manifestFor resolves a job's artifact list: the persisted manifest when
// present (and parseable), else a live scan of the result dir. It always
// returns a non-nil slice.
func manifestFor(res job.JobResult) []job.ArtifactItem {
	if res.ArtifactsJSON != "" {
		var items []job.ArtifactItem
		if err := json.Unmarshal([]byte(res.ArtifactsJSON), &items); err == nil && items != nil {
			return items
		}
		// Corrupt/empty manifest → fall through to a live scan rather than 500.
	}
	if items := job.ScanArtifacts(res.ResultDir); items != nil {
		return items
	}
	return []job.ArtifactItem{}
}
