package httpapi

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/settings"
)

// Extension settings (docs/design/2026-10-20-work-noise-and-db-settings-design.md):
// runtime-adjustable prompts, thresholds and exclusion lists in the database. Reading
// needs `settings.read` (seeded to `users`), writing needs `settings.write` (medium risk,
// not seeded); the route declarations carry both, so the ceiling gate already refuses a
// restricted caller's writes on a server-global route (authz_ceiling.go) while its reads
// pass. Every write is audited with the key and a value digest.

// settingsReady answers 503 when the server has no settings service, like the other
// optional services do.
func (s *Server) settingsReady(c *rux.Context) bool {
	if s.settings == nil {
		writeError(c, http.StatusServiceUnavailable, "settings unavailable", "no settings service wired on this server")
		return false
	}
	return true
}

// handleSettingsList: GET /v1/settings — every registered key with its default, current
// value, whether it was modified, its type and description.
func (s *Server) handleSettingsList(c *rux.Context) {
	if !s.settingsReady(c) {
		return
	}
	items, err := s.settings.List()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list settings failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"settings": items})
}

// handleSettingsGet: GET /v1/settings/{key}.
func (s *Server) handleSettingsGet(c *rux.Context) {
	if !s.settingsReady(c) {
		return
	}
	key := c.Param("key")
	items, err := s.settings.List()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read setting failed", err.Error())
		return
	}
	for _, item := range items {
		if item.Key == key {
			c.JSON(http.StatusOK, item)
			return
		}
	}
	writeError(c, http.StatusNotFound, "unknown setting", "no setting named "+key)
}

// settingsValueBody is the PUT body: {value: <string|number|bool|list>}. Value is kept
// untyped because the key's registered type decides what it may be.
type settingsValueBody struct {
	Value any `json:"value"`
}

// handleSettingsPut: PUT /v1/settings/{key} with {value}. The value is validated against
// the key's registered type and bounds; an invalid value is a 400 that names the reason.
func (s *Server) handleSettingsPut(c *rux.Context) {
	if !s.settingsReady(c) {
		return
	}
	key := c.Param("key")
	before, _ := s.settings.Get(key)
	var body settingsValueBody
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	item, _, err := s.settings.Set(key, body.Value, callerFromCtx(c))
	if err != nil {
		s.writeSettingsError(c, "write setting failed", err)
		return
	}
	s.auditSettingsWrite(c, "settings.set", key, before, item.Value)
	c.JSON(http.StatusOK, item)
}

// handleSettingsDelete: DELETE /v1/settings/{key} — drop the stored value, so the
// registered default applies again.
func (s *Server) handleSettingsDelete(c *rux.Context) {
	if !s.settingsReady(c) {
		return
	}
	key := c.Param("key")
	before, err := s.settings.Get(key)
	if err != nil {
		s.writeSettingsError(c, "reset setting failed", err)
		return
	}
	existed, err := s.settings.Reset(key)
	if err != nil {
		s.writeSettingsError(c, "reset setting failed", err)
		return
	}
	if existed {
		s.auditSettingsWrite(c, "settings.reset", key, before, nil)
	}
	after, err := s.settings.Get(key)
	if err != nil {
		s.writeSettingsError(c, "reset setting failed", err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"key": key, "modified": false, "value": after, "reset": existed})
}

// handleSettingsExport: GET /v1/settings/export — YAML of the modified keys only, so a
// backup / a move to another machine carries the operator's changes and nothing that is
// the binary's default.
func (s *Server) handleSettingsExport(c *rux.Context) {
	if !s.settingsReady(c) {
		return
	}
	doc, err := s.settings.Export()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "export settings failed", err.Error())
		return
	}
	c.SetHeader("Content-Type", "application/yaml; charset=utf-8")
	c.SetHeader("Content-Disposition", `attachment; filename="gofer-settings.yaml"`)
	c.Resp.WriteHeader(http.StatusOK)
	_, _ = c.Resp.Write(doc)
}

// handleSettingsImport: POST /v1/settings/import with a YAML body. `?dry_run=1` reports
// the per-key differences and validation results without writing anything; otherwise
// every key is validated and the valid ones are written.
func (s *Server) handleSettingsImport(c *rux.Context) {
	if !s.settingsReady(c) {
		return
	}
	raw, err := io.ReadAll(c.Req.Body)
	if err != nil {
		writeError(c, http.StatusBadRequest, "read import document failed", err.Error())
		return
	}
	dryRun := c.Query("dry_run") == "1" || strings.EqualFold(c.Query("dry_run"), "true")
	res, err := s.settings.Import(raw, dryRun, callerFromCtx(c))
	if err != nil {
		writeError(c, http.StatusBadRequest, "import settings failed", err.Error())
		return
	}
	if !dryRun {
		changed := make([]string, 0, len(res.Changes))
		for _, ch := range res.Changes {
			if ch.Action != "unchanged" && ch.Error == "" {
				changed = append(changed, ch.Key)
			}
		}
		if len(changed) > 0 {
			s.auditSettingsWrite(c, "settings.import", strings.Join(changed, ","), nil, nil)
		}
	}
	c.JSON(http.StatusOK, res)
}

// writeSettingsError maps a service error onto the HTTP status: an unknown key is a 404,
// a refused value a 400 with the reason.
func (s *Server) writeSettingsError(c *rux.Context, what string, err error) {
	var invalid *settings.ValidationError
	switch {
	case errors.Is(err, settings.ErrUnknownKey):
		writeError(c, http.StatusNotFound, "unknown setting", err.Error())
	case errors.As(err, &invalid):
		writeError(c, http.StatusBadRequest, what, invalid.Reason)
	default:
		writeError(c, http.StatusInternalServerError, what, err.Error())
	}
}

// auditSettingsWrite appends the settings write to auth_audit: who, which action and key,
// and a digest of the old and the new value. The digest of a prompt key is its length and
// first 40 characters — the prompt itself is long and may carry material that does not
// belong in an audit trail verbatim.
func (s *Server) auditSettingsWrite(c *rux.Context, action, key string, before, after any) {
	st := s.settingsAuditStore()
	if st == nil {
		return
	}
	parts := make([]string, 0, 2)
	if before != nil {
		parts = append(parts, "old="+settingsValueDigest(key, before))
	}
	if after != nil {
		parts = append(parts, "new="+settingsValueDigest(key, after))
	}
	if err := st.InsertAuthAudit(jobstore.AuthAuditEntry{
		Actor:      callerFromCtx(c),
		Action:     action,
		Subject:    key,
		Permission: authz.PermSettingsWrite,
		Method:     c.Req.Method,
		Path:       c.Req.URL.Path,
		Status:     http.StatusOK,
		Reason:     strings.Join(parts, " "),
	}); err != nil {
		slog.Warn("httpapi: settings audit", "action", action, "key", key, "err", err)
	}
}

// settingsAuditStore is the store the audit rows go to (nil without a job store).
func (s *Server) settingsAuditStore() *jobstore.Store {
	if s.jobs == nil {
		return nil
	}
	return s.jobs.Meta()
}

// settingsValueDigest renders a value for the audit trail: prompts as length + first 40
// characters, everything else verbatim (the lists and numbers are short).
func settingsValueDigest(key string, v any) string {
	str := settingsDisplayValue(v)
	if !strings.HasPrefix(key, "prompt.") {
		if len(str) > 200 {
			str = str[:200] + "…"
		}
		return str
	}
	head := str
	if len(head) > 40 {
		head = head[:40]
	}
	return "len=" + strconv.Itoa(len(str)) + " head=" + head
}

// settingsDisplayValue renders a setting value as the audit trail prints it.
func settingsDisplayValue(v any) string {
	if list, ok := v.([]string); ok {
		return "[" + strings.Join(list, ",") + "]"
	}
	return strings.TrimSpace(fmt.Sprintf("%v", v))
}
