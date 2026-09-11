package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/gookit/rux/v2"
	"github.com/inhere/gofer/internal/tunnel"
)

// tunnelRendezvousTimeout bounds the wait for a worker data connection.
var tunnelRendezvousTimeout = 15 * time.Second

// tunnelAuth resolves the bearer token to a caller entry.
func (s *Server) tunnelAuth(r *http.Request) (callerEntry, bool) {
	if len(s.callers) == 0 && s.allowEmptyToken {
		return callerEntry{id: "", kind: callerKindUser}, true
	}
	tok, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		return callerEntry{}, false
	}
	ce, ok := s.lookupCallerEntry(tok)
	return ce, ok
}

// handleTunnelConnect adapts the tunnelConnect HTTP handler to rux.
func (s *Server) handleTunnelConnect(c *rux.Context) { s.tunnelConnect(c.Resp, c.Req) }

// tunnelConnect authenticates a caller and bridges one client websocket to a worker tunnel.
func (s *Server) tunnelConnect(w http.ResponseWriter, r *http.Request) {
	fail := func(code int, reason string, attrs ...any) {
		slog.Warn("tunnel connect failed", append([]any{"status", code, "reason", reason}, attrs...)...)
		http.Error(w, reason, code)
	}
	ce, ok := s.tunnelAuth(r)
	if !ok {
		fail(http.StatusUnauthorized, "missing or invalid bearer token")
		return
	}
	if ce.kind == callerKindWorker {
		fail(http.StatusForbidden, "worker token cannot open tunnel", "caller", ce.id)
		return
	}
	if s.cfg != nil && s.cfg.Governance.RequireTunnelCapability && !s.cfg.CallerCanTunnel(ce.id) {
		fail(http.StatusForbidden, "caller lacks can_tunnel", "caller", ce.id)
		return
	}
	worker, target, network := r.URL.Query().Get("worker"), r.URL.Query().Get("target"), r.URL.Query().Get("network")
	if network == "" {
		network = "tcp"
	}
	if worker == "" || tunnel.ValidateTarget(target) != nil {
		fail(http.StatusBadRequest, "invalid worker or target", "caller", ce.id, "worker", worker, "target", target)
		return
	}
	if network != "tcp" {
		fail(http.StatusBadRequest, "network is not supported", "caller", ce.id, "worker", worker, "target", target)
		return
	}
	inst, live := s.hub.LiveInstance(worker)
	if !live {
		fail(http.StatusNotFound, "worker offline", "caller", ce.id, "worker", worker, "target", target)
		return
	}
	p, nonce := s.tunnels.Begin(tunnel.Binding{WorkerID: worker, InstanceID: inst, CallerID: ce.id, Target: target, ClientRemote: r.RemoteAddr}, tunnelRendezvousTimeout)
	if err := s.hub.OpenTunnel(worker, p.TunnelID(), network, target, nonce); err != nil {
		p.Cancel()
		code := http.StatusBadGateway
		if errors.Is(err, tunnel.ErrWorkerOffline) {
			code = http.StatusNotFound
		}
		if errors.Is(err, tunnel.ErrUnsupported) {
			code = http.StatusConflict
		}
		fail(code, err.Error(), "caller", ce.id, "worker", worker, "target", target, "tunnel_id", p.TunnelID())
		return
	}
	a, err := p.Wait(r.Context(), tunnelRendezvousTimeout)
	if err != nil {
		fail(http.StatusGatewayTimeout, err.Error(), "caller", ce.id, "worker", worker, "target", target, "tunnel_id", p.TunnelID())
		return
	}
	defer p.Finish()
	if a.Hello.ErrorCode != "" {
		fail(tunnel.HTTPStatusForCode(a.Hello.ErrorCode), a.Hello.Error, "caller", ce.id, "worker", worker, "target", target, "tunnel_id", p.TunnelID())
		_ = a.Conn.Close(websocket.StatusNormalClosure, "worker rejected")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		slog.Warn("tunnel connect failed", "tunnel_id", p.TunnelID(), "reason", err.Error(), "status", http.StatusBadGateway, "caller", ce.id, "worker", worker, "target", target)
		_ = a.Conn.Close(websocket.StatusNormalClosure, "client upgrade failed")
		return
	}
	conn.SetReadLimit(tunnel.ReadLimit)
	upd, rm := s.tunnels.Activate(tunnel.Info{ID: p.TunnelID(), CallerID: ce.id, WorkerID: worker, Target: target, ClientRemote: r.RemoteAddr, StartedAt: time.Now()})
	defer rm()
	defer conn.Close(websocket.StatusNormalClosure, "closed")
	defer a.Conn.Close(websocket.StatusNormalClosure, "closed")
	started := time.Now()
	slog.Info("tunnel opened", "tunnel_id", p.TunnelID(), "caller", ce.id, "worker", worker, "target", target, "client_remote", r.RemoteAddr)
	res := tunnel.Splice(r.Context(), conn, a.Conn, tunnel.SpliceOptions{OnProgress: upd})
	slog.Info("tunnel closed", "tunnel_id", p.TunnelID(), "caller", ce.id, "worker", worker, "target", target, "client_remote", r.RemoteAddr, "bytes_up", res.Up, "bytes_down", res.Down, "close_reason", res.Reason, "duration_ms", time.Since(started).Milliseconds(), "err", res.Err)
}

// handleWorkerTunnelConnectRaw authenticates worker callbacks outside the shared auth group and releases rendezvous resources before returning.
func (s *Server) handleWorkerTunnelConnectRaw(w http.ResponseWriter, r *http.Request) {
	ce, ok := s.tunnelAuth(r)
	if !ok || ce.kind != callerKindWorker {
		slog.Warn("worker tunnel failed", "status", http.StatusUnauthorized, "reason", "unauthorized")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		slog.Warn("worker tunnel failed", "status", http.StatusBadGateway, "reason", err.Error(), "caller", ce.id)
		return
	}
	defer conn.Close(websocket.StatusInternalError, "closed")
	conn.SetReadLimit(tunnel.ReadLimit)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var h tunnel.Hello
	if err := wsjson.Read(ctx, conn, &h); err != nil {
		slog.Warn("worker tunnel failed", "caller", ce.id, "close_code", int(websocket.StatusProtocolError), "reason", "hello read failed", "error", err)
		_ = conn.Close(websocket.StatusProtocolError, "expected hello")
		return
	}
	b, ok := s.tunnels.Consume(h.RelayNonce, time.Now())
	if !ok {
		slog.Warn("worker tunnel failed", "caller", ce.id, "tunnel_id", h.TunnelID, "status", 401, "close_code", 4401, "reason", "invalid nonce")
		_ = conn.Close(4401, "invalid nonce")
		return
	}
	if b.WorkerID != ce.id {
		slog.Warn("worker tunnel failed", "caller", ce.id, "tunnel_id", h.TunnelID, "status", 409, "close_code", 4409, "reason", "worker mismatch")
		_ = conn.Close(4409, "worker mismatch")
		return
	}
	if inst, live := s.hub.LiveInstance(b.WorkerID); !live || inst != b.InstanceID {
		slog.Warn("worker tunnel failed", "caller", ce.id, "tunnel_id", h.TunnelID, "status", 409, "close_code", 4409, "reason", "instance mismatch")
		_ = conn.Close(4409, "instance mismatch")
		return
	}
	if h.TunnelID != b.TunnelID {
		slog.Warn("worker tunnel failed", "caller", ce.id, "tunnel_id", h.TunnelID, "status", 404, "close_code", 4404, "reason", "tunnel mismatch")
		_ = conn.Close(4404, "tunnel mismatch")
		return
	}
	done, ok := s.tunnels.Deliver(h.TunnelID, tunnel.Arrival{Conn: conn, Hello: h})
	if !ok {
		slog.Warn("worker tunnel failed", "caller", ce.id, "tunnel_id", h.TunnelID, "status", 404, "close_code", 4404, "reason", "rendezvous gone")
		_ = conn.Close(4404, "gone")
		return
	}
	select {
	case <-done:
	case <-r.Context().Done():
	}
}

// handleWorkerTunnelConnect adapts the raw worker callback handler and preserves its connection lifetime contract.
func (s *Server) handleWorkerTunnelConnect(c *rux.Context) {
	s.handleWorkerTunnelConnectRaw(c.Resp, c.Req)
}

// handleListTunnels reports active tunnels using the same envelope shape as runners.
func (s *Server) handleListTunnels(c *rux.Context) {
	type tunnelView struct {
		ID           string    `json:"id"`
		CallerID     string    `json:"caller_id"`
		WorkerID     string    `json:"worker_id"`
		Target       string    `json:"target"`
		ClientRemote string    `json:"client_remote"`
		StartedAt    time.Time `json:"started_at"`
		BytesUp      int64     `json:"bytes_up"`
		BytesDown    int64     `json:"bytes_down"`
	}
	out := make([]tunnelView, 0)
	for _, i := range s.tunnels.List() {
		out = append(out, tunnelView{i.ID, i.CallerID, i.WorkerID, i.Target, i.ClientRemote, i.StartedAt, i.BytesUp, i.BytesDown})
	}
	c.JSON(http.StatusOK, map[string]any{"tunnels": out})
}
