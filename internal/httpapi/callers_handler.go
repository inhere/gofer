// 用户管理接口（配置入库 P1，设计 docs/design/2026-11-03-config-into-db-design.md
// §「鉴权」「新增用户」，计划 T6）。
//
// What lives here is ONLY the binding and the translation: the request bodies, the caller-kind
// gate every route repeats, and the mapping of a caller-administration result onto HTTP. A new
// user, a rotation, a state change and a limit change all run through Core's CallerAdmin seam
// (internal/core/callers.go): the transaction order, the token, the audit line and the
// re-merge of the published snapshot are the assembly layer's business (G021), and the entry
// layer never imports it — the seam is expressed with the config types both sides share.
//
// Two rules are repeated on EVERY route here, and both are load-bearing:
//
//   - `grantAdminOnly` runs FIRST. The route gate requires `auth.manage` (which only a root
//     administrator holds), but the gate deliberately does not judge a non-user caller, so a
//     worker token or a job credential would otherwise reach these handlers. A job credential
//     may read every GET route through SEC-01 (jobcredential.go), which must not hand it the
//     caller list or the audit.
//   - the token of a new / rotated caller appears in that ONE response and nowhere else: not in
//     the database (only its sha256), not in an audit line, not in a log (SR1301).
package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// CallerAdmin is the narrow seam the caller-administration routes ask for. *core.Core
// implements it; serve injects the singleton with SetCallers. A nil seam (mcp, most tests)
// leaves the routes mounted but answering 503 — the same degradation /v1/xfer and the config
// write surface use — so the router never has to be rebuilt for it.
type CallerAdmin interface {
	// AddCaller creates a stored caller and answers its token once.
	AddCaller(spec config.CallerSpec, actor string) (config.CallerChange, error)
	// RotateCaller replaces a stored caller's token and answers the new one once.
	RotateCaller(id, actor string) (config.CallerChange, error)
	// DisableCaller switches a stored caller off (idempotent).
	DisableCaller(id, actor string) (config.CallerChange, error)
	// EnableCaller brings a stored caller back; a caller a file entry took over must have been
	// rotated after the takeover.
	EnableCaller(id, actor string) (config.CallerChange, error)
	// SetCallerCeiling replaces (or, with noCeiling, removes) a caller's upper bound.
	SetCallerCeiling(id string, ceil *config.CallerCeiling, noCeiling bool, actor string) (config.CallerChange, error)
	// SetCallerLimits replaces a caller's quota overrides.
	SetCallerLimits(id string, lim config.CallerLimits, actor string) (config.CallerChange, error)
	// ListCallers reports every caller: file-declared, stored, disabled and taken over.
	ListCallers() ([]config.CallerView, error)
	// ListCallerAudit reports the config_audit lines of every caller change, newest first.
	ListCallerAudit(limit int) ([]jobstore.ConfigAuditEntry, error)
}

// callerAdmin returns the injected seam, or answers 503 when this server has none.
func (s *Server) callerAdmin(c *rux.Context) (CallerAdmin, bool) {
	if s.callers == nil {
		writeError(c, http.StatusServiceUnavailable, "caller management unavailable",
			"this server has no caller store (start `gofer serve` with the metadata store)")
		return nil, false
	}
	return s.callers, true
}

// callerAddReq is the body of POST /v1/callers: the id, the optional display name, the quota
// overrides, and the ceiling in exactly ONE of its two forms — a `ceiling` block, or
// `no_ceiling: true`. An empty `ceiling` block is refused (D16: it reads as a limit while
// restricting nothing), and so is giving neither.
type callerAddReq struct {
	ID                string                `json:"id"`
	Name              string                `json:"name"`
	MaxConcurrentJobs int                   `json:"max_concurrent_jobs"`
	RateLimit         float64               `json:"rate_limit"`
	RateBurst         int                   `json:"rate_burst"`
	Ceiling           *config.CallerCeiling `json:"ceiling"`
	NoCeiling         bool                  `json:"no_ceiling"`
}

// callerCeilingReq is the body of PUT /v1/callers/{id}/ceiling (same two forms as adding).
type callerCeilingReq struct {
	Ceiling   *config.CallerCeiling `json:"ceiling"`
	NoCeiling bool                  `json:"no_ceiling"`
}

// callerLimitsReq is the body of PUT /v1/callers/{id}/limits. Zero means "take the
// server.governance default", like every other per-caller quota.
type callerLimitsReq struct {
	MaxConcurrentJobs int     `json:"max_concurrent_jobs"`
	RateLimit         float64 `json:"rate_limit"`
	RateBurst         int     `json:"rate_burst"`
}

// callersView is GET /v1/callers: the caller list plus the route's own degradation note when
// the deployment has no metadata store to list from.
type callersView struct {
	Callers []config.CallerView `json:"callers"`
	Count   int                 `json:"count"`
}

// handleCallersList answers GET /v1/callers.
func (s *Server) handleCallersList(c *rux.Context) {
	if !s.grantAdminOnly(c) {
		return
	}
	admin, ok := s.callerAdmin(c)
	if !ok {
		return
	}
	views, err := admin.ListCallers()
	if err != nil {
		s.writeCallerError(c, "cannot list the callers", err)
		return
	}
	if views == nil {
		views = []config.CallerView{}
	}
	c.JSON(http.StatusOK, callersView{Callers: views, Count: len(views)})
}

// handleCallersAdd answers POST /v1/callers. The token it answers with is the only time the
// credential exists outside the caller's own hands.
func (s *Server) handleCallersAdd(c *rux.Context) {
	if !s.grantAdminOnly(c) {
		return
	}
	admin, ok := s.callerAdmin(c)
	if !ok {
		return
	}
	var req callerAddReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	change, err := admin.AddCaller(config.CallerSpec{
		ID: req.ID, Name: req.Name,
		MaxConcurrentJobs: req.MaxConcurrentJobs, RateLimit: req.RateLimit, RateBurst: req.RateBurst,
		Ceiling: req.Ceiling, NoCeiling: req.NoCeiling,
	}, callerFromCtx(c))
	if err != nil {
		s.writeCallerError(c, "cannot add the caller", err)
		return
	}
	c.JSON(http.StatusCreated, change)
}

// handleCallerRotate answers POST /v1/callers/{id}/rotate.
func (s *Server) handleCallerRotate(c *rux.Context) {
	if !s.grantAdminOnly(c) {
		return
	}
	admin, ok := s.callerAdmin(c)
	if !ok {
		return
	}
	change, err := admin.RotateCaller(c.Param("id"), callerFromCtx(c))
	if err != nil {
		s.writeCallerError(c, "cannot rotate the caller", err)
		return
	}
	// The credential the caller's open long connections were opened with is gone (design
	// §6「授权被撤销」 applies to a credential change too): re-judge them now.
	s.recheckLiveConns("caller rotated")
	c.JSON(http.StatusOK, change)
}

// handleCallerDisable answers POST /v1/callers/{id}/disable.
func (s *Server) handleCallerDisable(c *rux.Context) {
	if !s.grantAdminOnly(c) {
		return
	}
	admin, ok := s.callerAdmin(c)
	if !ok {
		return
	}
	change, err := admin.DisableCaller(c.Param("id"), callerFromCtx(c))
	if err != nil {
		s.writeCallerError(c, "cannot disable the caller", err)
		return
	}
	// A disabled caller's open tunnels / relays / streams end at once, without waiting for the
	// next sweep.
	s.recheckLiveConns("caller disabled")
	c.JSON(http.StatusOK, change)
}

// handleCallerEnable answers POST /v1/callers/{id}/enable.
func (s *Server) handleCallerEnable(c *rux.Context) {
	if !s.grantAdminOnly(c) {
		return
	}
	admin, ok := s.callerAdmin(c)
	if !ok {
		return
	}
	change, err := admin.EnableCaller(c.Param("id"), callerFromCtx(c))
	if err != nil {
		s.writeCallerError(c, "cannot enable the caller", err)
		return
	}
	c.JSON(http.StatusOK, change)
}

// handleCallerSetCeiling answers PUT /v1/callers/{id}/ceiling.
func (s *Server) handleCallerSetCeiling(c *rux.Context) {
	if !s.grantAdminOnly(c) {
		return
	}
	admin, ok := s.callerAdmin(c)
	if !ok {
		return
	}
	var req callerCeilingReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	change, err := admin.SetCallerCeiling(c.Param("id"), req.Ceiling, req.NoCeiling, callerFromCtx(c))
	if err != nil {
		s.writeCallerError(c, "cannot set the caller's ceiling", err)
		return
	}
	c.JSON(http.StatusOK, change)
}

// handleCallerSetLimits answers PUT /v1/callers/{id}/limits. A raised concurrency limit applies
// to the next job without a restart (the job layer re-syncs its semaphore per caller).
func (s *Server) handleCallerSetLimits(c *rux.Context) {
	if !s.grantAdminOnly(c) {
		return
	}
	admin, ok := s.callerAdmin(c)
	if !ok {
		return
	}
	var req callerLimitsReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	change, err := admin.SetCallerLimits(c.Param("id"),
		config.CallerLimitsOf(req.MaxConcurrentJobs, req.RateLimit, req.RateBurst), callerFromCtx(c))
	if err != nil {
		s.writeCallerError(c, "cannot set the caller's limits", err)
		return
	}
	c.JSON(http.StatusOK, change)
}

// callerAuditView is GET /v1/callers/audit. The rows carry the masked before/after values a
// caller change wrote — never a token, never its hash.
type callerAuditView struct {
	Entries []jobstore.ConfigAuditEntry `json:"entries"`
	Count   int                         `json:"count"`
}

// handleCallerAudit answers GET /v1/callers/audit?limit=.
func (s *Server) handleCallerAudit(c *rux.Context) {
	if !s.grantAdminOnly(c) {
		return
	}
	admin, ok := s.callerAdmin(c)
	if !ok {
		return
	}
	limit := 0
	if raw := strings.TrimSpace(c.Req.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(c, http.StatusBadRequest, "invalid limit",
				"limit must be a positive integer (at most "+strconv.Itoa(jobstore.MaxConfigAuditLimit)+")")
			return
		}
		limit = n
	}
	entries, err := admin.ListCallerAudit(limit)
	if err != nil {
		s.writeCallerError(c, "cannot list the caller audit", err)
		return
	}
	if entries == nil {
		entries = []jobstore.ConfigAuditEntry{}
	}
	c.JSON(http.StatusOK, callerAuditView{Entries: entries, Count: len(entries)})
}

// writeCallerError maps a caller-administration failure onto its status. The classification is
// the config package's (the sentinels the assembly layer wraps), so the message an operator
// sees is the one the write refused with — a 409 that does not say WHY (file caller? worker id?
// rotation needed?) leaves the operator with nothing to do next.
func (s *Server) writeCallerError(c *rux.Context, msg string, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, config.ErrCallerInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, config.ErrCallerNotFound):
		status = http.StatusNotFound
	case errors.Is(err, config.ErrCallerFileSource), errors.Is(err, config.ErrCallerIDTaken),
		errors.Is(err, config.ErrCallerRotationRequired):
		status = http.StatusConflict
	}
	writeError(c, status, msg, err.Error())
}
