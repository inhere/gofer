package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// tunnel_presets.go is the TUN-03 client for the two tunnel visibility surfaces:
// the forwarder registry (`gofer tun forward` announcing itself) and the server-side
// forward presets (`tun save` / `tun saved` / `tun forward -n` / `tun presets push`).
//
// The CLI talks to these over HTTP in both modes — a client node reaches the hub, and
// a machine-local `tun forward` registers with the same hub — so a preset is now the
// same preset on every machine that can reach the server.

// TunnelSpecView is one forwarding rule as the hub reports it. The hub stores rules
// structured; the CLI renders them.
type TunnelSpecView struct {
	Network   string `json:"network"`
	Bind      string `json:"bind"`
	LocalPort int    `json:"local_port"`
	Target    string `json:"target"`
}

// Display renders a rule the way `tun ls` shows it: `[udp/]lport -> target`, with a
// bind prefix only when it is not the default 127.0.0.1 (the common case needs no
// noise). ASCII `->` on purpose: the CLI is read in Windows consoles too.
func (v TunnelSpecView) Display() string {
	var b strings.Builder
	if v.Network == "udp" {
		b.WriteString("udp/")
	}
	if v.Bind != "" && v.Bind != "127.0.0.1" {
		b.WriteString(v.Bind)
		b.WriteString(":")
	}
	fmt.Fprintf(&b, "%d -> %s", v.LocalPort, v.Target)
	return b.String()
}

// TunnelForwarder is one online `gofer tun forward` process, with the traffic the hub
// attributed to it (connections grouped by caller+worker+target).
type TunnelForwarder struct {
	ID          string           `json:"id"`
	CallerID    string           `json:"caller_id"`
	Worker      string           `json:"worker"`
	Specs       []TunnelSpecView `json:"specs"`
	Host        string           `json:"host"`
	PID         int              `json:"pid"`
	StartedAt   time.Time        `json:"started_at"`
	LastSeenAt  time.Time        `json:"last_seen_at"`
	Connections int              `json:"connections"`
	BytesUp     int64            `json:"bytes_up"`
	BytesDown   int64            `json:"bytes_down"`
}

// TunnelForwarderRegistration is what a starting forwarder announces: the rules it is
// listening on and where it runs. The id and the caller are the hub's to assign.
type TunnelForwarderRegistration struct {
	Worker    string           `json:"worker"`
	Specs     []TunnelSpecView `json:"specs"`
	Host      string           `json:"host"`
	PID       int              `json:"pid"`
	StartedAt time.Time        `json:"started_at"`
}

// TunnelPreset is one saved forward preset on the server.
type TunnelPreset struct {
	Name      string    `json:"name"`
	Worker    string    `json:"worker"`
	Specs     []string  `json:"specs"`
	Note      string    `json:"note"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// RegisterTunnelForwarder announces a forwarder and returns the stored registration
// (with the hub-assigned id).
func (c *Client) RegisterTunnelForwarder(reg TunnelForwarderRegistration) (TunnelForwarder, error) {
	var out struct {
		Forwarder TunnelForwarder `json:"forwarder"`
	}
	body, err := json.Marshal(reg)
	if err != nil {
		return TunnelForwarder{}, fmt.Errorf("encode forwarder registration: %w", err)
	}
	err = c.doJSON(http.MethodPost, "/v1/tunnels/forwarders", bytes.NewReader(body), &out)
	return out.Forwarder, err
}

// HeartbeatTunnelForwarder renews a registration. specs may be nil to keep the rules
// the hub already holds.
func (c *Client) HeartbeatTunnelForwarder(id string, specs []TunnelSpecView) (TunnelForwarder, error) {
	var out struct {
		Forwarder TunnelForwarder `json:"forwarder"`
	}
	payload := struct {
		Specs []TunnelSpecView `json:"specs,omitempty"`
	}{Specs: specs}
	body, err := json.Marshal(payload)
	if err != nil {
		return TunnelForwarder{}, fmt.Errorf("encode forwarder heartbeat: %w", err)
	}
	err = c.doJSON(http.MethodPut, "/v1/tunnels/forwarders/"+url.PathEscape(id), bytes.NewReader(body), &out)
	return out.Forwarder, err
}

// UnregisterTunnelForwarder removes a registration (the forwarder's clean goodbye).
func (c *Client) UnregisterTunnelForwarder(id string) error {
	return c.doJSON(http.MethodDelete, "/v1/tunnels/forwarders/"+url.PathEscape(id), nil, nil)
}

// ListTunnelForwarders lists the online forwarders.
func (c *Client) ListTunnelForwarders() ([]TunnelForwarder, error) {
	var out struct {
		Forwarders []TunnelForwarder `json:"forwarders"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/tunnels/forwarders", nil, &out); err != nil {
		return nil, err
	}
	if out.Forwarders == nil {
		out.Forwarders = []TunnelForwarder{}
	}
	return out.Forwarders, nil
}

// ListTunnelPresets lists the server's presets.
func (c *Client) ListTunnelPresets() ([]TunnelPreset, error) {
	var out struct {
		Presets []TunnelPreset `json:"presets"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/tunnels/presets", nil, &out); err != nil {
		return nil, err
	}
	if out.Presets == nil {
		out.Presets = []TunnelPreset{}
	}
	return out.Presets, nil
}

// GetTunnelPreset reads one preset. A missing name is a *StatusError with Status 404,
// which is what the local-fallback path branches on.
func (c *Client) GetTunnelPreset(name string) (TunnelPreset, error) {
	var out struct {
		Preset TunnelPreset `json:"preset"`
	}
	err := c.doJSON(http.MethodGet, "/v1/tunnels/presets/"+url.PathEscape(name), nil, &out)
	return out.Preset, err
}

// PutTunnelPreset writes one preset. Without force the server refuses to replace an
// existing name (409), which `tun save` surfaces as "use --force".
func (c *Client) PutTunnelPreset(name string, p TunnelPreset, force bool) (TunnelPreset, error) {
	var out struct {
		Preset TunnelPreset `json:"preset"`
	}
	body, err := json.Marshal(struct {
		Worker string   `json:"worker"`
		Specs  []string `json:"specs"`
		Note   string   `json:"note,omitempty"`
		Force  bool     `json:"force,omitempty"`
	}{Worker: p.Worker, Specs: p.Specs, Note: p.Note, Force: force})
	if err != nil {
		return TunnelPreset{}, fmt.Errorf("encode tunnel preset: %w", err)
	}
	err = c.doJSON(http.MethodPut, "/v1/tunnels/presets/"+url.PathEscape(name), bytes.NewReader(body), &out)
	return out.Preset, err
}

// DeleteTunnelPreset removes one preset.
func (c *Client) DeleteTunnelPreset(name string) error {
	return c.doJSON(http.MethodDelete, "/v1/tunnels/presets/"+url.PathEscape(name), nil, nil)
}
