package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tunnel"
)

type localTunnelPresetView struct {
	Name      string   `json:"name"`
	Worker    string   `json:"worker"`
	Specs     []string `json:"specs"`
	Note      string   `json:"note"`
	Autostart bool     `json:"autostart"`
}

func (s *Server) hostedWriteCaller(c *rux.Context) (bool, bool) {
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, "worker token cannot manage hosted tunnel forwarders", "a worker token may not start, stop or import a hosted forwarder")
		return false, false
	}
	return true, true
}

func (s *Server) handleStartHostedForwarder(c *rux.Context) {
	if ok, _ := s.hostedWriteCaller(c); !ok {
		return
	}
	if s.hostedForwarders == nil {
		writeError(c, http.StatusServiceUnavailable, "hosted tunnel manager unavailable", "server-local tunnel forwarding is not wired")
		return
	}
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
	specs, err := tunnelPresetSpecs(rec)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid tunnel preset", err.Error())
		return
	}
	reg, err := s.hostedForwarders.Start(rec.Name, callerFromCtx(c), rec.Worker, specs)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, tunnel.ErrForwarderNotFound) {
			status = http.StatusNotFound
		}
		writeError(c, status, "start hosted forward failed", err.Error())
		return
	}
	resp := map[string]any{"forwarder": s.forwarderViewOf(reg)}
	// Design §6: the forwarder is a long connection of its STARTER, so losing tunnel.hosted or
	// tunnel.open (or a caller a reload removed) stops it instead of leaving a listener behind
	// the permission that was taken away.
	s.trackHostedForwarder(rec.Name, s.requestLiveConnSpec(c, liveConnHosted, rec.Name))
	if s.hub == nil {
		resp["warning"] = fmt.Sprintf("worker %q 状态未知；监听已启动，首次连接时按现有转发行为拨号", rec.Worker)
	} else if _, live := s.hub.LiveInstance(rec.Worker); !live {
		resp["warning"] = fmt.Sprintf("worker %q 当前不在线；监听已启动，worker 重连后新连接才会拨号", rec.Worker)
	}
	c.JSON(http.StatusOK, resp)
}

// handleStopHostedForwarder stops a hosted forwarder (DELETE /v1/tunnels/hosted/{name}).
//
// The manager records WHO STARTED each hosted forwarder, and that starter owns it: its own
// forwarder needs tunnel.hosted + tunnel.open (the route gate decided those), anyone else's
// needs tunnel.manage.others — 仅根 (design §1「tunnel.manage.others：停他人的转发与托管转发」).
func (s *Server) handleStopHostedForwarder(c *rux.Context) {
	if ok, _ := s.hostedWriteCaller(c); !ok {
		return
	}
	if s.hostedForwarders == nil {
		writeError(c, http.StatusServiceUnavailable, "hosted tunnel manager unavailable", "server-local tunnel forwarding is not wired")
		return
	}
	starter, running := s.hostedForwarders.Starter(c.Param("name"))
	if !running {
		writeError(c, http.StatusNotFound, "hosted forward not running", fmt.Sprintf("hosted forward %q is not running", c.Param("name")))
		return
	}
	if !s.mayStopHosted(c, starter) {
		s.denyHostedStop(c, starter)
		return
	}
	if err := s.hostedForwarders.Stop(c.Param("name")); err != nil {
		writeError(c, http.StatusNotFound, "hosted forward not running", err.Error())
		return
	}
	s.forgetLiveConn(hostedLiveConnKey(c.Param("name")))
	c.JSON(http.StatusOK, map[string]any{"stopped": true})
}

func tunnelPresetSpecs(rec jobstore.TunnelPresetRecord) ([]tunnel.ForwardSpec, error) {
	var raw []string
	if rec.SpecsJSON == "" {
		return nil, errors.New("preset has no forward specs")
	}
	if err := json.Unmarshal([]byte(rec.SpecsJSON), &raw); err != nil {
		return nil, fmt.Errorf("decode preset specs: %w", err)
	}
	flat, err := tunnel.SplitSpecs(raw)
	if err != nil {
		return nil, err
	}
	return tunnel.ParseSpecs(flat)
}

func (s *Server) handleListLocalTunnelPresets(c *rux.Context) {
	st, ok := s.tunnelPresetStore(c)
	if !ok {
		return
	}
	local, err := config.LoadTunnels()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read local tunnel presets failed", err.Error())
		return
	}
	remote, err := st.ListTunnelPresets()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list tunnel presets failed", err.Error())
		return
	}
	seen := make(map[string]struct{}, len(remote))
	for _, p := range remote {
		seen[p.Name] = struct{}{}
	}
	names := make([]string, 0, len(local.Forwards))
	for name := range local.Forwards {
		if _, exists := seen[name]; !exists {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]localTunnelPresetView, 0, len(names))
	for _, name := range names {
		p := local.Forwards[name]
		out = append(out, localTunnelPresetView{Name: name, Worker: p.Worker, Specs: p.Specs, Note: p.Note, Autostart: p.Autostart})
	}
	c.JSON(http.StatusOK, map[string]any{"presets": out})
}

func (s *Server) handleImportLocalTunnelPreset(c *rux.Context) {
	if ok, _ := s.hostedWriteCaller(c); !ok {
		return
	}
	st, ok := s.tunnelPresetStore(c)
	if !ok {
		return
	}
	local, err := config.LoadTunnels()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read local tunnel presets failed", err.Error())
		return
	}
	p, found := local.Forwards[c.Param("name")]
	if !found {
		writeError(c, http.StatusNotFound, "local tunnel preset not found", "no local preset named "+c.Param("name"))
		return
	}
	profile, err := config.NormalizeTunnelProfile(p)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid local tunnel preset", err.Error())
		return
	}
	if err := config.ValidateTunnelProfile(c.Param("name"), profile); err != nil {
		writeError(c, http.StatusBadRequest, "invalid local tunnel preset", err.Error())
		return
	}
	if _, exists, err := st.GetTunnelPreset(c.Param("name")); err != nil {
		writeError(c, http.StatusInternalServerError, "read tunnel preset failed", err.Error())
		return
	} else if exists {
		writeError(c, http.StatusConflict, "tunnel preset already exists", "same-name import follows `tun presets push` and is skipped")
		return
	}
	b, err := json.Marshal(profile.Specs)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "encode preset failed", err.Error())
		return
	}
	rec := jobstore.TunnelPresetRecord{Name: c.Param("name"), Worker: profile.Worker, SpecsJSON: string(b), Note: profile.Note, Autostart: profile.Autostart, UpdatedAt: time.Now().Unix(), UpdatedBy: callerFromCtx(c)}
	if err := st.UpsertTunnelPreset(rec); err != nil {
		writeError(c, http.StatusInternalServerError, "import tunnel preset failed", err.Error())
		return
	}
	v, _ := tunnelPresetViewOf(rec)
	c.JSON(http.StatusOK, map[string]any{"preset": v, "imported": true})
}
