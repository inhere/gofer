package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/tunnel"
)

// tunnelRendezvousTimeout bounds the wait for a worker data connection.
var tunnelRendezvousTimeout = 15 * time.Second

// tunnelAuth resolves the bearer token to a caller entry.
func (s *Server) tunnelAuth(r *http.Request) (callerEntry, bool) {
	empty := len(s.callerEntries()) == 0
	if empty && s.allowEmptyToken {
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
func (s *Server) handleTunnelConnect(c *rux.Context) { s.tunnelConnect(c) }

// tunnelConnect authenticates a caller and bridges one client websocket to a worker tunnel.
//
// It runs OUTSIDE the /v1 auth group (a WS handshake cannot use the JSON error envelope),
// so the route gate never resolves a caller for it: the handler is the authority on the
// identity — it reads the bearer token itself, stamps the resolved caller onto the request
// context and then asks the ONE permission of the design's tunnel.open row through the very
// same s.can every other route uses (design §1「tunnel.open：开隧道连接」, and the calling
// assertion sees the decision).
func (s *Server) tunnelConnect(c *rux.Context) {
	w, r := c.Resp, c.Req
	fail := func(code int, reason string, attrs ...any) {
		slog.Warn("tunnel.rejected", append([]any{"event", "tunnel.rejected", "component", "server", "error_code", "http_rejected", "status", code, "error", reason}, attrs...)...)
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
	c.Set(ctxCallerID, ce.id)
	c.Set(ctxCallerKind, ce.kind)
	if !s.can(c, authz.PermTunnelOpen) {
		fail(http.StatusForbidden, "permission required: "+authz.PermTunnelOpen, "caller", ce.id)
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
	if network != "tcp" && network != "udp" {
		fail(http.StatusBadRequest, "network is not supported", "caller", ce.id, "worker", worker, "target", target)
		return
	}
	inst, live := s.hub.LiveInstance(worker)
	if !live {
		fail(http.StatusNotFound, "worker offline", "caller", ce.id, "worker", worker, "target", target)
		return
	}
	p, nonce := s.tunnels.Begin(tunnel.Binding{WorkerID: worker, InstanceID: inst, CallerID: ce.id, Target: target, ClientRemote: r.RemoteAddr}, tunnelRendezvousTimeout)
	tunnelID := p.TunnelID()
	w.Header().Set(tunnel.HeaderTunnelID, tunnelID)
	slog.Info("tunnel.requested", "event", "tunnel.requested", "component", "server", "tunnel_id", tunnelID, "network", network, "target", target, "worker_id", worker, "caller", ce.id, "client_remote", r.RemoteAddr)
	rendezvousStart := time.Now()
	slog.Info("tunnel.rendezvous_started", "event", "tunnel.rendezvous_started", "component", "server", "tunnel_id", tunnelID, "worker_id", worker)
	if err := s.hub.OpenTunnel(worker, tunnelID, network, target, nonce); err != nil {
		p.Cancel()
		code := http.StatusBadGateway
		if errors.Is(err, tunnel.ErrWorkerOffline) {
			code = http.StatusNotFound
		}
		if errors.Is(err, tunnel.ErrUnsupported) {
			code = http.StatusConflict
		}
		fail(code, err.Error(), "caller", ce.id, "worker", worker, "target", target, "tunnel_id", tunnelID)
		return
	}
	a, err := p.Wait(r.Context(), tunnelRendezvousTimeout)
	if err != nil {
		fail(http.StatusGatewayTimeout, err.Error(), "caller", ce.id, "worker", worker, "target", target, "tunnel_id", tunnelID)
		return
	}
	defer p.Finish()
	if a.Hello.ErrorCode != "" {
		fail(tunnel.HTTPStatusForCode(a.Hello.ErrorCode), a.Hello.Error, "caller", ce.id, "worker", worker, "target", target, "tunnel_id", tunnelID, "error_code", a.Hello.ErrorCode)
		_ = a.Conn.Close(websocket.StatusNormalClosure, "worker rejected")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		slog.Error("tunnel.error", "event", "tunnel.error", "component", "server", "tunnel_id", tunnelID, "error", err, "status", http.StatusBadGateway)
		_ = a.Conn.Close(websocket.StatusNormalClosure, "client upgrade failed")
		return
	}
	conn.SetReadLimit(tunnel.ReadLimit)
	upd, rm := s.tunnels.Activate(tunnel.Info{ID: p.TunnelID(), CallerID: ce.id, WorkerID: worker, Target: target, ClientRemote: r.RemoteAddr, StartedAt: time.Now()})
	defer rm()
	defer conn.Close(websocket.StatusNormalClosure, "closed")
	defer a.Conn.Close(websocket.StatusNormalClosure, "closed")
	started := time.Now()
	slog.Info("tunnel.worker_connected", "event", "tunnel.worker_connected", "component", "server", "tunnel_id", tunnelID, "worker_id", worker, "rendezvous_ms", time.Since(rendezvousStart).Milliseconds())
	slog.Info("tunnel.opened", "event", "tunnel.opened", "component", "server", "tunnel_id", tunnelID, "network", network, "caller", ce.id, "worker_id", worker, "target", target, "client_remote", r.RemoteAddr)
	res := tunnel.Splice(r.Context(), conn, a.Conn, tunnel.SpliceOptions{OnProgress: upd,
		OnFirstUp: func() {
			slog.Info("tunnel.first_up", "event", "tunnel.first_up", "component", "server", "tunnel_id", tunnelID, "network", network, "target", target, "first_byte_ms", time.Since(started).Milliseconds())
		},
		OnFirstDown: func() {
			slog.Info("tunnel.first_down", "event", "tunnel.first_down", "component", "server", "tunnel_id", tunnelID, "network", network, "target", target, "first_byte_ms", time.Since(started).Milliseconds())
		},
	})
	slog.Info("tunnel.closed", "event", "tunnel.closed", "component", "server", "tunnel_id", tunnelID, "caller", ce.id, "worker_id", worker, "target", target, "client_remote", r.RemoteAddr, "bytes_up", res.Up, "bytes_down", res.Down, "close_reason", res.Reason, "duration_ms", time.Since(started).Milliseconds(), "error", res.Err)
}

// handleWorkerTunnelConnectRaw authenticates worker callbacks outside the shared auth group and releases rendezvous resources before returning.
func (s *Server) handleWorkerTunnelConnectRaw(w http.ResponseWriter, r *http.Request) {
	ce, ok := s.tunnelAuth(r)
	if !ok || ce.kind != callerKindWorker {
		slog.Warn("tunnel.rejected", "event", "tunnel.rejected", "component", "server", "side", "worker", "error_code", "worker_unauthorized", "status", http.StatusUnauthorized, "reason", "unauthorized")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		slog.Warn("tunnel.rejected", "event", "tunnel.rejected", "component", "server", "side", "worker", "error_code", "worker_upgrade_failed", "status", http.StatusBadGateway, "reason", err.Error(), "caller", ce.id)
		return
	}
	defer conn.Close(websocket.StatusInternalError, "closed")
	conn.SetReadLimit(tunnel.ReadLimit)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var h tunnel.Hello
	if err := wsjson.Read(ctx, conn, &h); err != nil {
		slog.Warn("tunnel.rejected", "event", "tunnel.rejected", "component", "server", "side", "worker", "error_code", "hello_read_failed", "caller", ce.id, "close_code", int(websocket.StatusProtocolError), "reason", "hello read failed", "error", err)
		_ = conn.Close(websocket.StatusProtocolError, "expected hello")
		return
	}
	b, ok := s.tunnels.Consume(h.RelayNonce, time.Now())
	if !ok {
		slog.Warn("tunnel.rejected", "event", "tunnel.rejected", "component", "server", "side", "worker", "error_code", "invalid_nonce", "caller", ce.id, "tunnel_id", h.TunnelID, "status", 401, "close_code", 4401, "reason", "invalid nonce")
		_ = conn.Close(4401, "invalid nonce")
		return
	}
	if b.WorkerID != ce.id {
		slog.Warn("tunnel.rejected", "event", "tunnel.rejected", "component", "server", "side", "worker", "error_code", "worker_mismatch", "caller", ce.id, "tunnel_id", h.TunnelID, "status", 409, "close_code", 4409, "reason", "worker mismatch")
		_ = conn.Close(4409, "worker mismatch")
		return
	}
	if inst, live := s.hub.LiveInstance(b.WorkerID); !live || inst != b.InstanceID {
		slog.Warn("tunnel.rejected", "event", "tunnel.rejected", "component", "server", "side", "worker", "error_code", "instance_mismatch", "caller", ce.id, "tunnel_id", h.TunnelID, "status", 409, "close_code", 4409, "reason", "instance mismatch")
		_ = conn.Close(4409, "instance mismatch")
		return
	}
	if h.TunnelID != b.TunnelID {
		slog.Warn("tunnel.rejected", "event", "tunnel.rejected", "component", "server", "side", "worker", "error_code", "tunnel_mismatch", "caller", ce.id, "tunnel_id", h.TunnelID, "status", 404, "close_code", 4404, "reason", "tunnel mismatch")
		_ = conn.Close(4404, "tunnel mismatch")
		return
	}
	done, ok := s.tunnels.Deliver(h.TunnelID, tunnel.Arrival{Conn: conn, Hello: h})
	if !ok {
		slog.Warn("tunnel.rejected", "event", "tunnel.rejected", "component", "server", "side", "worker", "error_code", "rendezvous_gone", "caller", ce.id, "tunnel_id", h.TunnelID, "status", 404, "close_code", 4404, "reason", "rendezvous gone")
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

// dialHostedTunnel opens a tunnel for a HOSTED forwarder IN PROCESS (design §6「托管转发改为
// 进程内拨号，不再借 server 自己的 token 走 tunnelConnect；判定与登记都按启动者」).
//
// The HTTP way of opening a tunnel would make the SERVER the caller: it dials its own
// /v1/tunnels/connect with the server's own bearer token, so the tunnel is judged and recorded
// as the server's caller (default) — the hosted forwarder's starter is never asked, and with
// require_tunnel_capability on the server's own caller may not even hold the capability.
// Here the rendezvous (s.tunnels / s.hub) is driven directly, the caller-side websocket is an
// in-process pair (tunnel.PipePair) and the server-side end is spliced with the worker's data
// connection exactly like the HTTP endpoint does. Everything is keyed by callerID, the STARTER
// of the forwarder:
//
//   - tunnel.open is decided for the starter (the same checkpoint the HTTP endpoint is, and
//     the per-connection half of the design's hosted rule; the route gate decided it at start);
//   - the active tunnel is registered with CallerID = starter, so the forwarded connections
//     count towards that caller's forwarder in GET /v1/tunnels/forwarders;
//   - the logs name the starter, never the server.
//
// The long-lived splice is NOT tied to ctx: the Forwarder cancels its per-connection dial
// context as soon as the dial returns, and the tunnel has to outlive it (it ends when either
// side closes, like the HTTP path).
func (s *Server) dialHostedTunnel(ctx context.Context, callerID, worker, network, target string) (tunnel.DialResult, error) {
	if s.hub == nil {
		return tunnel.DialResult{}, errors.New("hosted tunnel dial: no worker hub is wired")
	}
	if network != "tcp" && network != "udp" {
		return tunnel.DialResult{}, fmt.Errorf("hosted tunnel dial: network %q is not supported", network)
	}
	if err := tunnel.ValidateTarget(target); err != nil {
		return tunnel.DialResult{}, err
	}
	if !s.mayOpenTunnel(callerID) {
		return tunnel.DialResult{}, fmt.Errorf("hosted tunnel dial: caller %q lacks %s", callerID, authz.PermTunnelOpen)
	}
	inst, live := s.hub.LiveInstance(worker)
	if !live {
		return tunnel.DialResult{}, tunnel.ErrWorkerOffline
	}
	client, server, err := tunnel.PipePair(ctx)
	if err != nil {
		return tunnel.DialResult{}, fmt.Errorf("hosted tunnel dial: in-process pair: %w", err)
	}
	p, nonce := s.tunnels.Begin(tunnel.Binding{WorkerID: worker, InstanceID: inst, CallerID: callerID, Target: target, ClientRemote: "hosted"}, tunnelRendezvousTimeout)
	tunnelID := p.TunnelID()
	slog.Info("tunnel.requested", "event", "tunnel.requested", "component", "server", "tunnel_id", tunnelID, "network", network, "target", target, "worker_id", worker, "caller", callerID, "hosted", true)
	if err := s.hub.OpenTunnel(worker, tunnelID, network, target, nonce); err != nil {
		p.Cancel()
		closePipePair(client, server)
		return tunnel.DialResult{}, err
	}
	a, err := p.Wait(ctx, tunnelRendezvousTimeout)
	if err != nil {
		closePipePair(client, server)
		return tunnel.DialResult{}, err
	}
	if a.Hello.ErrorCode != "" {
		p.Cancel()
		_ = a.Conn.Close(websocket.StatusNormalClosure, "worker rejected")
		closePipePair(client, server)
		return tunnel.DialResult{}, fmt.Errorf("hosted tunnel dial: worker rejected (%s): %s", a.Hello.ErrorCode, a.Hello.Error)
	}
	upd, rm := s.tunnels.Activate(tunnel.Info{ID: tunnelID, CallerID: callerID, WorkerID: worker, Target: target, ClientRemote: "hosted", StartedAt: time.Now()})
	started := time.Now()
	slog.Info("tunnel.opened", "event", "tunnel.opened", "component", "server", "tunnel_id", tunnelID, "network", network, "caller", callerID, "worker_id", worker, "target", target, "hosted", true)
	go func() {
		defer rm()
		defer p.Finish()
		defer closePipePair(client, server)
		defer func() { _ = a.Conn.Close(websocket.StatusNormalClosure, "closed") }()
		res := tunnel.Splice(context.Background(), server, a.Conn, tunnel.SpliceOptions{OnProgress: upd,
			OnFirstUp: func() {
				slog.Info("tunnel.first_up", "event", "tunnel.first_up", "component", "server", "tunnel_id", tunnelID, "network", network, "target", target, "caller", callerID, "hosted", true)
			},
			OnFirstDown: func() {
				slog.Info("tunnel.first_down", "event", "tunnel.first_down", "component", "server", "tunnel_id", tunnelID, "network", network, "target", target, "caller", callerID, "hosted", true)
			},
		})
		slog.Info("tunnel.closed", "event", "tunnel.closed", "component", "server", "tunnel_id", tunnelID, "caller", callerID, "worker_id", worker, "target", target, "hosted", true, "bytes_up", res.Up, "bytes_down", res.Down, "close_reason", res.Reason, "duration_ms", time.Since(started).Milliseconds(), "error", res.Err)
	}()
	return tunnel.DialResult{Conn: client, TunnelID: tunnelID}, nil
}

// closePipePair releases both ends of an in-process pair whose tunnel never came up.
func closePipePair(client, server *websocket.Conn) {
	_ = client.CloseNow()
	_ = server.CloseNow()
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
