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
	p := cl.tunnelPolicy.Load()
	code, msg := "", ""
	var nc net.Conn
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
			nc, err = net.DialTimeout(network, t.Target, p.timeout)
			if err != nil {
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
		slog.Warn("tunnel rejected", "worker_id", cl.workerID, "tunnel_id", t.TunnelID,
			"target", t.Target, "error_code", code, "error", msg)
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
		slog.Warn("tunnel data connection to server failed", "worker_id", cl.workerID,
			"tunnel_id", t.TunnelID, "err", err)
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
	slog.Info("tunnel opened", "worker_id", cl.workerID, "tunnel_id", t.TunnelID, "target", t.Target)
	started := time.Now()
	var fromDevice, toDevice int64
	var e error
	if network == "udp" {
		fromDevice, toDevice, e = tunnel.DatagramBridge(ctx, ws, nc)
	} else {
		fromDevice, toDevice, e = tunnel.Bridge(ctx, ws, nc)
	}
	slog.Info("tunnel closed", "worker_id", cl.workerID, "tunnel_id", t.TunnelID, "target", t.Target,
		"bytes_from_device", fromDevice, "bytes_to_device", toDevice,
		"duration_ms", time.Since(started).Milliseconds(), "error", e)
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
