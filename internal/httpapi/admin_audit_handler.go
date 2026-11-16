package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/jobstore"
)

// parseAuditTime reads a `since` / `until` value: unix seconds, RFC 3339, or a plain date
// (the start of that day, UTC). until given as a plain date means the END of that day.
func parseAuditTime(raw string, endOfDay bool) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
		return n, true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Unix(), true
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		if endOfDay {
			t = t.Add(24*time.Hour - time.Second)
		}
		return t.Unix(), true
	}
	return 0, false
}

// handleAdminAudit answers GET /v1/audit (audit.read): newest first, filtered by
// `caller`, `route` (substring of the route template), `status` (ok | fail), `since` /
// `until`, paged by `cursor` (the next_cursor of the previous page) and `limit`.
// A non-person credential (a job or worker credential) never reads the trail.
func (s *Server) handleAdminAudit(c *rux.Context) {
	if callerKindFromCtx(c) != callerKindUser {
		writeError(c, http.StatusForbidden, "not permitted for this caller",
			"only a person's credential may read the admin audit")
		return
	}
	st := s.authStore()
	if st == nil {
		writeError(c, http.StatusServiceUnavailable, "audit unavailable", "this server has no metadata store")
		return
	}
	// A line recorded a moment ago may still be queued.
	s.FlushAudit()
	q := c.Req.URL.Query()
	f := jobstore.AdminAuditFilter{
		Caller: q.Get("caller"), Route: q.Get("route"), Status: strings.ToLower(strings.TrimSpace(q.Get("status"))),
	}
	if f.Status != "" && f.Status != "ok" && f.Status != "fail" {
		writeError(c, http.StatusBadRequest, "invalid status", "status must be ok or fail")
		return
	}
	for _, p := range []struct {
		name string
		dst  *int64
		end  bool
	}{{"since", &f.Since, false}, {"until", &f.Until, true}} {
		if raw := strings.TrimSpace(q.Get(p.name)); raw != "" {
			v, ok := parseAuditTime(raw, p.end)
			if !ok {
				writeError(c, http.StatusBadRequest, "invalid "+p.name, p.name+" must be unix seconds, an RFC 3339 time or a YYYY-MM-DD date")
				return
			}
			*p.dst = v
		}
	}
	if raw := strings.TrimSpace(q.Get("cursor")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			writeError(c, http.StatusBadRequest, "invalid cursor", "cursor must be the next_cursor of a previous answer")
			return
		}
		f.Cursor = n
	}
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(c, http.StatusBadRequest, "invalid limit",
				"limit must be a positive integer (at most "+strconv.Itoa(jobstore.MaxAdminAuditLimit)+")")
			return
		}
		f.Limit = n
	}
	entries, next, err := st.ListAdminAudit(f)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "cannot list the audit", err.Error())
		return
	}
	if entries == nil {
		entries = []jobstore.AdminAuditEntry{}
	}
	c.JSON(http.StatusOK, struct {
		Entries    []jobstore.AdminAuditEntry `json:"entries"`
		Count      int                        `json:"count"`
		NextCursor int64                      `json:"next_cursor,omitempty"`
	}{Entries: entries, Count: len(entries), NextCursor: next})
}
