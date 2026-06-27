package httpapi

import (
	"errors"
	"net/http"
	"os"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/project"
)

// project_browse_handler.go exposes the Web 控制台 v2 只读层 endpoints (design §7):
// E20 project git status, E32 sub-git discovery and E32 whitelisted key-file
// reads. Each handler is a thin wrapper: 404-guard the project key, delegate to
// internal/project/browse.go, encode the result. All business/safety logic
// (fixed git args, SafeJoin, whitelist, size/binary caps) lives in browse.go.

// handleGetProjectGit serves GET /v1/projects/{key}/git (E20). An unknown key is
// a 404; a non-git / non-locally-reachable root returns 200 with is_git_repo:false.
func (s *Server) handleGetProjectGit(c *rux.Context) {
	key := c.Param("key")
	proj, err := s.projects.Get(key)
	if err != nil {
		writeError(c, http.StatusNotFound, "unknown project", err.Error())
		return
	}
	st, err := project.ProjectGit(s.projects.Config(), proj)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "git status failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, st)
}

// handleListRepos serves GET /v1/projects/{key}/repos (E32 sub-git discovery).
// An unknown key is a 404; the result is {"repos":[...]} (always a non-null array).
func (s *Server) handleListRepos(c *rux.Context) {
	key := c.Param("key")
	proj, err := s.projects.Get(key)
	if err != nil {
		writeError(c, http.StatusNotFound, "unknown project", err.Error())
		return
	}
	repos, err := project.DiscoverRepos(s.projects.Config(), proj)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "repo discovery failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"repos": repos})
}

// handleGetProjectFile serves GET /v1/projects/{key}/file?path=<rel> (E32 key
// file). Error mapping (design §7): unknown key / missing path → 404 / 400;
// non-whitelisted basename → 403 (ErrForbiddenFile); traversal or missing file →
// 404; binary file → 415 (ErrBinary).
func (s *Server) handleGetProjectFile(c *rux.Context) {
	key := c.Param("key")
	proj, err := s.projects.Get(key)
	if err != nil {
		writeError(c, http.StatusNotFound, "unknown project", err.Error())
		return
	}
	rel := c.Query("path")
	if rel == "" {
		writeError(c, http.StatusBadRequest, "missing path", "query param 'path' is required")
		return
	}

	fc, err := project.ReadKeyFile(s.projects.Config(), proj, rel)
	if err != nil {
		switch {
		case errors.Is(err, project.ErrForbiddenFile):
			writeError(c, http.StatusForbidden, "forbidden file", "only whitelisted key files may be read")
		case errors.Is(err, project.ErrBinary):
			writeError(c, http.StatusUnsupportedMediaType, "binary file", "file is not text and cannot be previewed")
		case errors.Is(err, os.ErrNotExist):
			writeError(c, http.StatusNotFound, "no such file", "file not found")
		default:
			// SafeJoin escapes and other path errors → 404 with a generic detail
			// (do not leak resolved absolute paths).
			writeError(c, http.StatusNotFound, "no such file", "file not found")
		}
		return
	}
	c.JSON(http.StatusOK, fc)
}
