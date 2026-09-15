package worker

import (
	"context"
	"fmt"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tunnel"
	"github.com/inhere/gofer/internal/wsproto"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"
)

type tunnelPolicy struct {
	allow   *tunnel.Allowlist
	max     int
	timeout time.Duration
}

func outTunnel(c config.WorkerTunnelConfig) *tunnelPolicy {
	a, _ := tunnel.ParseAllowlist(c.Allow)
	return &tunnelPolicy{a, c.EffectiveMaxConns(), c.DialTimeout()}
}
func (cl *Client) applyTunnel(p *tunnelPolicy) { cl.tunnelPolicy.Store(p) }
func (cl *Client) handleTunnelOpen(ctx context.Context, sessionURL string, t wsproto.TunnelOpen) {
	slog.Info("tunnel.requested", "event", "tunnel.requested", "component", "worker", "tunnel_id", t.TunnelID, "network", t.Network, "target", t.Target, "worker_id", cl.workerID)
	dialStarted := time.Now()
	p := cl.tunnelPolicy.Load()
	code, msg := "", ""
	var nc net.Conn
	var pc net.PacketConn
	var udpTarget net.Addr
	network := t.Network
	if network == "" {
		network = "tcp"
	}
	if network != "tcp" && network != "udp" {
		code = tunnel.CodeBadTarget
		msg = fmt.Sprintf("network %s unsupported", t.Network)
	} else if p == nil || p.allow.Empty() {
		code = tunnel.CodeDisabled
		msg = "tunnel disabled"
	} else if err := tunnel.ValidateTarget(t.Target); err != nil {
		code = tunnel.CodeBadTarget
		msg = err.Error()
	} else if !p.allow.AllowsNetwork(network, t.Target) {
		code = tunnel.CodeNotAllowed
		msg = "target not allowed"
	} else {
		cl.tunnelMu.Lock()
		if cl.tunnelActive >= p.max {
			code = tunnel.CodeLimit
			msg = "too many tunnels"
		} else {
			cl.tunnelActive++
		}
		cl.tunnelMu.Unlock()
		if code == "" {
			defer func() { cl.tunnelMu.Lock(); cl.tunnelActive--; cl.tunnelMu.Unlock() }()
			var err error
			if network == "udp" {
				// UDP uses an UNCONNECTED socket: a connected one would drop a reply
				// that the device sends from a different port, which is common enough
				// on industrial gear to look like a dead tunnel. DatagramBridge keeps
				// the authorization boundary by only accepting the target's own IP.
				if udpTarget, err = net.ResolveUDPAddr("udp", t.Target); err != nil {
					code = tunnel.CodeBadTarget
					msg = err.Error()
				} else if pc, err = net.ListenPacket("udp", ":0"); err != nil {
					code = tunnel.CodeDialFailed
					msg = err.Error()
				} else {
					defer pc.Close()
				}
			} else if nc, err = net.DialTimeout(network, t.Target, p.timeout); err != nil {
				code = tunnel.CodeDialFailed
				msg = err.Error()
			} else {
				// Close the device connection on every exit path (bad hub URL, data ws
				// dial or hello failure); Bridge closes it too and a second Close is harmless.
				defer nc.Close()
			}
		}
	}
	if code != "" {
		slog.Warn("tunnel.rejected", "event", "tunnel.rejected", "component", "worker", "worker_id", cl.workerID, "tunnel_id", t.TunnelID,
			"network", network, "target", t.Target, "error_code", code, "error", msg, "dial_ms", time.Since(dialStarted).Milliseconds())
	}
	u, err := deriveConnectURL(sessionURL, tunnel.WorkerConnectPath)
	if err != nil {
		return
	}
	h := http.Header{}
	if cl.token != "" {
		h.Set("Authorization", "Bearer "+cl.token)
	}
	ws, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		slog.Error("tunnel.error", "event", "tunnel.error", "component", "worker", "worker_id", cl.workerID,
			"tunnel_id", t.TunnelID, "error", err)
		return
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	ws.SetReadLimit(tunnel.ReadLimit)
	hello := tunnel.Hello{TunnelID: t.TunnelID, RelayNonce: t.RelayNonce, ErrorCode: code, Error: msg}
	if err = wsjson.Write(ctx, ws, hello); err != nil {
		return
	}
	if code != "" {
		return
	}
	started := time.Now()
	slog.Info("tunnel.opened", "event", "tunnel.opened", "component", "worker", "worker_id", cl.workerID, "tunnel_id", t.TunnelID, "network", network, "target", t.Target, "dial_ms", time.Since(dialStarted).Milliseconds())
	var fromDevice, toDevice int64
	var reason string
	var e error
	firstUp := func() {
		slog.Info("tunnel.first_up", "event", "tunnel.first_up", "component", "worker", "worker_id", cl.workerID, "tunnel_id", t.TunnelID, "network", network, "target", t.Target, "first_byte_ms", time.Since(started).Milliseconds())
	}
	firstDown := func() {
		slog.Info("tunnel.first_down", "event", "tunnel.first_down", "component", "worker", "worker_id", cl.workerID, "tunnel_id", t.TunnelID, "network", network, "target", t.Target, "first_byte_ms", time.Since(started).Milliseconds())
	}
	var packets []any // udp only: datagram counts ride along on tunnel.closed
	if network == "udp" {
		r := tunnel.DatagramBridgeWithOptions(ctx, ws, pc, udpTarget, tunnel.BridgeOptions{OnFirstUp: firstUp, OnFirstDown: firstDown})
		fromDevice, toDevice, reason, e = r.ToWS, r.FromWS, r.Reason, r.Err
		packets = []any{"packets_down", r.PacketsToWS, "packets_up", r.PacketsFromWS}
	} else {
		r := tunnel.BridgeWithOptions(ctx, ws, nc, tunnel.BridgeOptions{OnFirstUp: firstUp, OnFirstDown: firstDown})
		fromDevice, toDevice, reason, e = r.ToWS, r.FromWS, r.Reason, r.Err
	}
	closed := []any{"event", "tunnel.closed", "component", "worker", "worker_id", cl.workerID, "tunnel_id", t.TunnelID, "network", network, "target", t.Target,
		"bytes_down", fromDevice, "bytes_up", toDevice,
		"duration_ms", time.Since(started).Milliseconds(), "close_reason", reason, "error", e}
	slog.Info("tunnel.closed", append(closed, packets...)...)
}
func deriveConnectURL(raw, path string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" {
		return "", fmt.Errorf("bad hub url")
	}
	u.Path = path
	u.RawQuery = ""
	return u.String(), nil
}
