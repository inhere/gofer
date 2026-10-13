package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/updatecheck"
)

// SetUpdateChecker injects the release checker (gofer-6x2n). Without one the update
// endpoints answer 503.
func (s *Server) SetUpdateChecker(c *updatecheck.Checker) { s.updater = c }

// handleUpdateStatus returns the latest release check (checked_at 0 = never checked).
func (s *Server) handleUpdateStatus(c *rux.Context) {
	if s.updater == nil {
		writeError(c, http.StatusServiceUnavailable, "update check unavailable", "this server has no release checker")
		return
	}
	c.JSON(http.StatusOK, s.updater.Status())
}

// handleUpdateCheck runs a release check now. A failure of the source is a 200 with
// the error in the result (the check itself worked; the answer is "could not tell").
func (s *Server) handleUpdateCheck(c *rux.Context) {
	if s.updater == nil {
		writeError(c, http.StatusServiceUnavailable, "update check unavailable", "this server has no release checker")
		return
	}
	c.JSON(http.StatusOK, s.updater.Check(c.Req.Context()))
}
