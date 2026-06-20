package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

	base := filepath.Join(res.ResultDir, "artifacts")
	full, err := safeJoinUnder(base, c.Param("name"))
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

// safeJoinUnder joins a request-supplied relative name under base and guarantees
// the result stays inside base (design §9 — the artifact download path check):
//   - empty / absolute names are rejected outright;
//   - the name is anchored to root and Cleaned ("/"+name) so any "../" is folded
//     away before joining (a "../etc" can never climb above base);
//   - a Rel check rejects any path that still escapes (defence in depth);
//   - EvalSymlinks resolves the real target and re-checks containment, so a
//     symlink inside artifacts/ pointing outside is rejected (symlink escape).
//
// It mirrors the口径 of project.SafeJoin (internal/project/path.go).
func safeJoinUnder(base, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", errors.New("artifact name must be a non-empty relative path")
	}
	// Normalize backslashes so a Windows-style "sub\\b" is treated as a path
	// separator on Linux too.
	normalized := strings.ReplaceAll(name, "\\", "/")

	// Reject traversal that escapes the dir explicitly (clearer than silently
	// folding "../foo" into "foo"): a name whose Cleaned form is ".." or begins
	// with "../" climbs above base. Internal "x/../y" Cleans to "y" → allowed.
	if cleaned := filepath.Clean(normalized); cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("artifact name escapes artifacts dir")
	}

	// Anchor to root + Clean to strip any residual "..", then Rel-check that the
	// join stays inside base (defence in depth on top of the segment check).
	full := filepath.Join(base, filepath.Clean("/"+normalized))
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("artifact name escapes artifacts dir")
	}

	// Symlink escape: if the (existing) target resolves outside base, reject.
	// A non-existent path (EvalSymlinks errors) is handled by the caller's Stat
	// (404), so a missing file is not a 400 here.
	if real, err := filepath.EvalSymlinks(full); err == nil {
		realBase, errB := filepath.EvalSymlinks(base)
		if errB != nil {
			realBase = base // base should exist; fall back to the lexical base.
		}
		r2, err2 := filepath.Rel(realBase, real)
		if err2 != nil || r2 == ".." || strings.HasPrefix(r2, ".."+string(filepath.Separator)) {
			return "", errors.New("artifact name escapes artifacts dir (symlink)")
		}
	}
	return full, nil
}
