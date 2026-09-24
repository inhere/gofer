package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tunnel"
)

// TUN-03: the server-side forward-preset store.
//
// Presets used to live only in <config-dir>/tunnels.yaml on whichever machine ran
// `gofer tun save` — invisible to the console and re-entered on every new machine. They
// now live here, so `tun save` writes once and any operator (or the web form) sees the
// same set.
//
// The rules stay STRINGS (the CLI's own spelling, one rule per entry): the server
// validates them with the very same config.ValidateTunnelProfile the local file uses,
// so a preset cannot be accepted here and refused there — and a comma-joined list is
// split on the way in, never stored compound.

// TunnelPresetStore is the narrow persistence seam for those presets, the same shape
// PtySessionStore uses: the entry layer asks for four operations and *jobstore.Store
// satisfies it. A nil store leaves the routes mounted but answering 503 (mcp / most
// tests), which is the degradation /v1/xfer and the config writes already use.
type TunnelPresetStore interface {
	UpsertTunnelPreset(rec jobstore.TunnelPresetRecord) error
	GetTunnelPreset(name string) (jobstore.TunnelPresetRecord, bool, error)
	ListTunnelPresets() ([]jobstore.TunnelPresetRecord, error)
	DeleteTunnelPreset(name string) error
}

// SetTunnelPresets injects the preset store (serve wires the metadata store). It mounts
// no routes, so it does not rebuild the router.
func (s *Server) SetTunnelPresets(st TunnelPresetStore) { s.tunnelPresets = st }

// forwarderTTL is the live registration lifetime (TUN-03). It is read on every registry
// read/write rather than copied at construction, so editing
// server.tunnel.forwarder_ttl_sec takes effect on the next request.
func (s *Server) forwarderTTL() time.Duration {
	if s.cfg == nil {
		return tunnel.DefaultForwarderTTL
	}
	return s.cfg.Tunnel.EffectiveForwarderTTL()
}

// tunnelPresetBody is the PUT body. `force` is the `tun save --force` decision: without
// it, replacing an existing name is refused (409) so a typo cannot silently overwrite a
// working preset.
type tunnelPresetBody struct {
	Worker string   `json:"worker"`
	Specs  []string `json:"specs"`
	Note   string   `json:"note,omitempty"`
	Force  bool     `json:"force,omitempty"`
}

// tunnelPresetView is one preset on the wire. updated_at / updated_by are the write
// audit the console shows.
type tunnelPresetView struct {
	Name      string    `json:"name"`
	Worker    string    `json:"worker"`
	Specs     []string  `json:"specs"`
	Note      string    `json:"note"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// handleListTunnelPresets lists the stored presets (GET /v1/tunnels/presets).
func (s *Server) handleListTunnelPresets(c *rux.Context) {
	st, ok := s.tunnelPresetStore(c)
	if !ok {
		return
	}
	recs, err := st.ListTunnelPresets()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list tunnel presets failed", err.Error())
		return
	}
	out := make([]tunnelPresetView, 0, len(recs))
	for _, rec := range recs {
		v, ok := tunnelPresetViewOf(rec)
		if !ok {
			// A row whose specs_json cannot be decoded is a corrupted row, not a 500 for
			// the whole list: report it with no rules rather than hiding every other
			// preset behind it.
			v = tunnelPresetView{Name: rec.Name, Worker: rec.Worker, Note: rec.Note,
				UpdatedAt: time.Unix(rec.UpdatedAt, 0), UpdatedBy: rec.UpdatedBy, Specs: []string{}}
		}
		out = append(out, v)
	}
	c.JSON(http.StatusOK, map[string]any{"presets": out})
}

// handleGetTunnelPreset reads one preset (GET /v1/tunnels/presets/{name}).
func (s *Server) handleGetTunnelPreset(c *rux.Context) {
	st, ok := s.tunnelPresetStore(c)
	if !ok {
		return
	}
	rec, found, err := st.GetTunnelPreset(c.Param("name"))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read tunnel preset failed", err.Error())
		return
	}
	if !found {
		writeError(c, http.StatusNotFound, "tunnel preset not found", "no preset named "+c.Param("name")+" on this server")
		return
	}
	v, _ := tunnelPresetViewOf(rec)
	c.JSON(http.StatusOK, map[string]any{"preset": v})
}

// handlePutTunnelPreset writes one preset (PUT /v1/tunnels/presets/{name}).
func (s *Server) handlePutTunnelPreset(c *rux.Context) {
	st, ok := s.tunnelPresetStore(c)
	if !ok {
		return
	}
	name := c.Param("name")
	var body tunnelPresetBody
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	// The comma form is a way to WRITE several rules (TUN-04): split first, then judge
	// what will actually run — the same order `tun save` uses locally.
	profile, err := config.NormalizeTunnelProfile(config.TunnelProfile{
		Worker: body.Worker, Specs: body.Specs, Note: body.Note,
	})
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid preset", "at least one forward spec is required")
		return
	}
	if err := config.ValidateTunnelProfile(name, profile); err != nil {
		writeError(c, http.StatusBadRequest, "invalid preset", err.Error())
		return
	}
	_, exists, err := st.GetTunnelPreset(name)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read tunnel preset failed", err.Error())
		return
	}
	if exists && !body.Force {
		writeError(c, http.StatusConflict, "tunnel preset already exists",
			"tunnel preset "+name+" already exists; re-send with force to overwrite it")
		return
	}
	specsJSON, err := json.Marshal(profile.Specs)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "encode preset failed", err.Error())
		return
	}
	now := time.Now()
	rec := jobstore.TunnelPresetRecord{
		Name: name, Worker: profile.Worker, SpecsJSON: string(specsJSON), Note: profile.Note,
		UpdatedAt: now.Unix(), UpdatedBy: callerFromCtx(c),
	}
	if err := st.UpsertTunnelPreset(rec); err != nil {
		writeError(c, http.StatusInternalServerError, "save tunnel preset failed", err.Error())
		return
	}
	v, _ := tunnelPresetViewOf(rec)
	c.JSON(http.StatusOK, map[string]any{"preset": v})
}

// handleDeleteTunnelPreset removes one preset (DELETE /v1/tunnels/presets/{name}).
func (s *Server) handleDeleteTunnelPreset(c *rux.Context) {
	st, ok := s.tunnelPresetStore(c)
	if !ok {
		return
	}
	if err := st.DeleteTunnelPreset(c.Param("name")); err != nil {
		// An absent name is the caller's 404; anything else is the server's 500 (a
		// storage failure is not "there is no such preset").
		if errors.Is(err, jobstore.ErrTunnelPresetNotFound) {
			writeError(c, http.StatusNotFound, "tunnel preset not found", err.Error())
			return
		}
		writeError(c, http.StatusInternalServerError, "delete tunnel preset failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"deleted": true})
}

// tunnelPresetStore returns the store, or answers 503 when this server has none.
func (s *Server) tunnelPresetStore(c *rux.Context) (TunnelPresetStore, bool) {
	if s.tunnelPresets == nil {
		writeError(c, http.StatusServiceUnavailable, "tunnel preset store unavailable",
			"this server has no tunnel preset store wired; start it with `gofer serve`")
		return nil, false
	}
	return s.tunnelPresets, true
}

// tunnelPresetViewOf renders a stored record. ok=false means the row's specs_json could
// not be decoded.
func tunnelPresetViewOf(rec jobstore.TunnelPresetRecord) (tunnelPresetView, bool) {
	specs := []string{}
	if rec.SpecsJSON != "" {
		if err := json.Unmarshal([]byte(rec.SpecsJSON), &specs); err != nil {
			return tunnelPresetView{}, false
		}
	}
	return tunnelPresetView{
		Name: rec.Name, Worker: rec.Worker, Specs: specs, Note: rec.Note,
		UpdatedAt: time.Unix(rec.UpdatedAt, 0), UpdatedBy: rec.UpdatedBy,
	}, true
}
