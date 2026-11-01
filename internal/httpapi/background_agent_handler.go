package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/bgagent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// Background agents (design 2026-10-30-job-check-agent-design.md §5.4): a read-only,
// server-wide view of the sessions and today's totals of the server-owned agents
// (summarizer, steward, job check), and a manual "start a new session". Reading needs
// `settings.read`, the reset `settings.write`; both are a person's (a job credential and a
// worker token are refused), and a caller with a project ceiling is refused because the
// agents belong to no project. The reset only marks the session book (the next run is fresh);
// it changes no configuration and stops nothing that is running.

// backgroundAgentsReady checks the caller and returns the job store behind the status, or
// answers the refusal.
func (s *Server) backgroundAgentsReady(c *rux.Context, what string) (*jobstore.Store, bool) {
	if !adminUserOnly(c, what) {
		return nil, false
	}
	if s.ceilingActive(c) {
		writeError(c, http.StatusForbidden, "background agents denied",
			"a caller with a project ceiling cannot "+what+": the background agents are server-wide and belong to no project")
		return nil, false
	}
	st := s.settingsAuditStore()
	if st == nil {
		writeError(c, http.StatusServiceUnavailable, "background agents unavailable", "no job store wired on this server")
		return nil, false
	}
	return st, true
}

// backgroundKindStatus reads one kind. The summarizer asks its own service (its policy keeps
// the legacy overrides and its day boundary is the server's local day); the other kinds use
// the unified thresholds of server.background_agents.
func (s *Server) backgroundKindStatus(r *bgagent.Runner, kind string) (bgagent.KindStatus, error) {
	if kind == bgagent.KindSummarizer && s.work != nil {
		return s.work.BackgroundStatus()
	}
	var bg config.BackgroundAgentsConfig
	if s.projects != nil {
		if cfg := s.projects.Config(); cfg != nil {
			bg = cfg.Server.BackgroundAgents
		}
	}
	return r.Status(kind, bgagent.PolicyFromConfig(bg.Session))
}

// handleBackgroundAgentsList: GET /v1/background-agents.
func (s *Server) handleBackgroundAgentsList(c *rux.Context) {
	st, ok := s.backgroundAgentsReady(c, "read the background agents")
	if !ok {
		return
	}
	r := bgagent.New(st, bgagent.Options{})
	statuses := make([]bgagent.KindStatus, 0, len(bgagent.Kinds()))
	for _, kind := range bgagent.Kinds() {
		ks, err := s.backgroundKindStatus(r, kind)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "read background agents failed", err.Error())
			return
		}
		statuses = append(statuses, ks)
	}
	c.JSON(http.StatusOK, bgagent.NewView(statuses...))
}

// handleBackgroundAgentSessionReset: POST /v1/background-agents/{kind}/session/reset.
func (s *Server) handleBackgroundAgentSessionReset(c *rux.Context) {
	st, ok := s.backgroundAgentsReady(c, "reset a background agent session")
	if !ok {
		return
	}
	kind := c.Param("kind")
	if !bgagent.KnownKind(kind) {
		writeError(c, http.StatusNotFound, "unknown background agent", "no background agent kind named "+kind)
		return
	}
	actor := callerFromCtx(c)
	r := bgagent.New(st, bgagent.Options{})
	n, err := r.ResetKind(kind, actor)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "reset background agent session failed", err.Error())
		return
	}
	s.auditBackgroundReset(c, st, kind, n)
	ks, err := s.backgroundKindStatus(r, kind)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read background agents failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, bgagent.ResetView{Version: bgagent.ViewVersion, Reset: n, Kind: bgagent.NewKindView(ks)})
}

// auditBackgroundReset appends the reset to auth_audit (who, which kind, how many sessions);
// the runner already leaves its own background_agent.reset event per session scope.
func (s *Server) auditBackgroundReset(c *rux.Context, st *jobstore.Store, kind string, n int64) {
	if err := st.InsertAuthAudit(jobstore.AuthAuditEntry{
		Actor:      callerFromCtx(c),
		Action:     "background_agent.reset",
		Subject:    kind,
		Permission: authz.PermSettingsWrite,
		Method:     c.Req.Method,
		Path:       c.Req.URL.Path,
		Status:     http.StatusOK,
		Reason:     "sessions=" + strconv.FormatInt(n, 10),
	}); err != nil {
		slog.Warn("httpapi: background agent audit", "kind", kind, "err", err)
	}
}
