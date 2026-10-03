package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"
	"github.com/inhere/gofer/internal/jobstore"
)

type jobSecretScanRequest struct {
	Literals []string `json:"literals,omitempty"`
	Patterns []string `json:"patterns,omitempty"`
	Project  string   `json:"project,omitempty"`
	Since    int64    `json:"since,omitempty"`
}

// handleSecretScan searches terminal job text without returning matched text.
// Non-admin callers are scoped to their own jobs; administrators may use the
// optional project/time filters across the server.
func (s *Server) handleSecretScan(c *rux.Context) {
	var req jobSecretScanRequest
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid secret scan request", "body must contain literals and/or patterns")
		return
	}
	filter := jobstore.SecretScanFilter{ProjectKey: req.Project, SinceUnix: req.Since}
	caller := callerFromCtx(c)
	if s.cfg == nil || !s.cfg.CallerCanAdmin(caller) {
		filter.OwnerID = caller
	}
	report, err := s.jobs.Meta().ScanSecrets(req.Literals, req.Patterns, filter)
	if err != nil {
		writeError(c, http.StatusBadRequest, "secret scan failed", redactSafeError(err))
		return
	}
	c.JSON(http.StatusOK, report)
}
