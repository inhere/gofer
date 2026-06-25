package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/store"
)

// handleListArtifacts returns a job's artifact manifest ([{name,size,mtime}]).
// The manifest resolution (persisted job.ArtifactsJSON preferred, else a live
// scan of <result_dir>/artifacts/) lives in job.Service.GetArtifactManifest; the
// handler only maps an unknown id to a 404. The array is always non-nil, so an
// empty result serialises as {"artifacts":[]}.
func (s *Server) handleListArtifacts(c *rux.Context) {
	id := c.Param("id")
	m, ok := s.jobs.GetArtifactManifest(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"artifacts": m.Items})
}

// handleDownloadArtifact streams a single artifact file. The {name} route param
// is a catch-all (registered as {name:.+}) so it carries subdir-relative names
// like "sub/b.bin". The name is path-safe joined under <result_dir>/artifacts/
// (the largest new external file-serving surface — see design §9): traversal,
// absolute paths and symlink escapes are rejected with 400. A missing file or a
// directory target is 404.
func (s *Server) handleDownloadArtifact(c *rux.Context) {
	id := c.Param("id")
	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}

	// P4 (D6): a job executed on a remote machine回传 only its artifact MANIFEST
	// (clear from the list endpoint), not the file bytes — the大产物文件留 worker
	// 侧 / peer 侧. So a download of a remote artifact is not served from the host
	// (its ResultDir holds no artifacts). Return a clear 409 the Web标注 ("留在
	// worker / peer 执行机") instead of a misleading generic 404. The remote-source
	// judgement is the data-plane logic moved to job.IsRemoteSource.
	if job.IsRemoteSource(res.Source) {
		writeError(c, http.StatusConflict, "remote artifact",
			"this job ran on "+res.Source+"; its artifact files stay on the execution machine (v1 returns the manifest only)")
		return
	}

	base := filepath.Join(res.ResultDir, "artifacts")
	full, err := store.SafeJoinUnder(base, c.Param("name"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid artifact path", err.Error())
		return
	}
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() {
		writeError(c, http.StatusNotFound, "no such artifact", "artifact not found")
		return
	}

	c.SetHeader("Content-Disposition", "attachment; filename="+strconv.Quote(filepath.Base(full)))
	http.ServeFile(c.Resp, c.Req, full)
}
