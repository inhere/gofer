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
	Redact   bool     `json:"redact,omitempty"`
	Yes      bool     `json:"yes,omitempty"`
	Vacuum   bool     `json:"vacuum,omitempty"`
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
	if req.Redact {
		if !req.Yes {
			writeError(c, http.StatusBadRequest, "secret redaction requires confirmation", "set yes=true to apply redaction")
			return
		}
		report, err := s.jobs.Meta().RedactSecrets(req.Literals, req.Patterns, filter, req.Vacuum)
		if err != nil {
			writeError(c, http.StatusBadRequest, "secret redaction failed", redactSafeError(err))
			return
		}
		c.JSON(http.StatusOK, report)
		return
	}
	report, err := s.jobs.Meta().ScanSecrets(req.Literals, req.Patterns, filter)
	if err != nil {
		writeError(c, http.StatusBadRequest, "secret scan failed", redactSafeError(err))
		return
	}
	c.JSON(http.StatusOK, report)
}
