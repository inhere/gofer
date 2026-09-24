package httpapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/tunnel"
)

// TUN-03: the forwarder registry surface.
//
// A `gofer tun forward` process listens on a CLIENT machine, so the hub otherwise only
// sees the connections that happen to be running: with nothing connected, the console
// showed an empty tunnel page even though three forwarders were up. The command now
// announces itself here (POST), renews every 30s (PUT) and says goodbye on exit
// (DELETE); the hub keeps that in memory with a TTL, so a killed forwarder disappears
// on its own.
//
// It is DISPLAY state. Registering grants nothing: a connection still authenticates
// against the bearer token + can_tunnel, and the worker still applies its own tunnel
// allowlist (the same boundary SEC-02 drew).

// defaultForwarderRegistryTTL is only a fallback for a hand-built Server (New always
// wires the config-backed func).
var defaultForwarderRegistryTTL = func() time.Duration { return tunnel.DefaultForwarderTTL }

// forwarderSpecBody is one rule as a client sends it. It mirrors tunnel.ForwardSpec
// with an explicit snake_case JSON spelling (the config structs carry yaml tags only,
// which encoding/json cannot match against underscored keys).
type forwarderSpecBody struct {
	Network string `json:"network"`
	Bind    string `json:"bind"`
	// LocalPort 0 is a valid spec (an ephemeral listener, as `ParseForwardSpec`
	// accepts), so the field is not required to be non-zero.
	LocalPort int    `json:"local_port"`
	Target    string `json:"target"`
}

// forwarderRegisterBody is the POST body. The identity is NOT in it: the caller comes
// from the bearer token and the id is minted here.
type forwarderRegisterBody struct {
	Worker string              `json:"worker"`
	Specs  []forwarderSpecBody `json:"specs"`
	Host   string              `json:"host"`
	PID    int                 `json:"pid"`
	// StartedAt is the forwarder's own start time (its clock), used for the uptime the
	// console shows; the hub's LastSeenAt is a separate field so a clock skew on the
	// client cannot make a live forwarder look expired.
	StartedAt time.Time `json:"started_at"`
}

// forwarderHeartbeatBody is the PUT body: the latest rules, if they changed. The
// identity rides in the path.
type forwarderHeartbeatBody struct {
	Specs []forwarderSpecBody `json:"specs"`
}

// forwarderSpecView is one rule on the wire.
type forwarderSpecView struct {
	Network   string `json:"network"`
	Bind      string `json:"bind"`
	LocalPort int    `json:"local_port"`
	Target    string `json:"target"`
}

// forwarderView is one online forwarder plus the roll-up of the active connections
// that belong to it.
type forwarderView struct {
	ID       string              `json:"id"`
	CallerID string              `json:"caller_id"`
	Worker   string              `json:"worker"`
	Specs    []forwarderSpecView `json:"specs"`
	Host     string              `json:"host"`
	PID      int                 `json:"pid"`
	// StartedAt is the forwarder's uptime base; LastSeenAt lets the console show how
	// fresh the registration is (and is what the TTL counts from).
	StartedAt  time.Time `json:"started_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	// Connections / BytesUp / BytesDown are the (caller, worker, target)-grouped
	// traffic for this registration.
	Connections int   `json:"connections"`
	BytesUp     int64 `json:"bytes_up"`
	BytesDown   int64 `json:"bytes_down"`
}

// handleListTunnelForwarders reports the online forwarders with their traffic roll-up
// (GET /v1/tunnels/forwarders).
func (s *Server) handleListTunnelForwarders(c *rux.Context) {
	regs, ok := s.forwarderRegistry(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, map[string]any{"forwarders": s.buildForwarderViews(regs.List())})
}

// handleRegisterTunnelForwarder registers a freshly started forwarder
// (POST /v1/tunnels/forwarders).
func (s *Server) handleRegisterTunnelForwarder(c *rux.Context) {
	reg, ok := s.forwarderRegistry(c)
	if !ok {
		return
	}
	caller, ok := s.forwarderWriteCaller(c)
	if !ok {
		return
	}
	var body forwarderRegisterBody
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if strings.TrimSpace(body.Worker) == "" {
		writeError(c, http.StatusBadRequest, "worker required", "a forwarder registration must name the worker its rules run through")
		return
	}
	specs, err := parseForwarderSpecs(body.Specs)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid forward spec", err.Error())
		return
	}
	stored := reg.Register(caller, tunnel.ForwarderRegistration{
		Worker:    body.Worker,
		Specs:     specs,
		Host:      body.Host,
		PID:       body.PID,
		StartedAt: body.StartedAt,
	})
	c.JSON(http.StatusOK, map[string]any{"forwarder": s.forwarderViewOf(stored)})
}

// handleHeartbeatTunnelForwarder renews a registration (PUT /v1/tunnels/forwarders/{id}).
func (s *Server) handleHeartbeatTunnelForwarder(c *rux.Context) {
	reg, ok := s.forwarderRegistry(c)
	if !ok {
		return
	}
	caller, ok := s.forwarderWriteCaller(c)
	if !ok {
		return
	}
	var body forwarderHeartbeatBody
	// An empty body is a plain "still alive" ping (curl -X PUT with no payload), which
	// must keep the existing rules rather than be refused as malformed.
	if err := c.BindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	var specs []tunnel.ForwardSpec
	if len(body.Specs) > 0 {
		var err error
		if specs, err = parseForwarderSpecs(body.Specs); err != nil {
			writeError(c, http.StatusBadRequest, "invalid forward spec", err.Error())
			return
		}
	}
	stored, err := reg.Heartbeat(c.Param("id"), caller, specs)
	if err != nil {
		writeForwarderRegistryError(c, err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"forwarder": s.forwarderViewOf(stored)})
}

// handleDeleteTunnelForwarder removes a registration
// (DELETE /v1/tunnels/forwarders/{id}) — the forwarder's clean goodbye.
func (s *Server) handleDeleteTunnelForwarder(c *rux.Context) {
	reg, ok := s.forwarderRegistry(c)
	if !ok {
		return
	}
	caller, ok := s.forwarderWriteCaller(c)
	if !ok {
		return
	}
	if err := reg.Delete(c.Param("id"), caller); err != nil {
		writeForwarderRegistryError(c, err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"deleted": true})
}

// forwarderRegistry returns the registry, or answers 503 when this server has none
// (only a hand-built Server; New always wires one) — the same degradation /v1/xfer and
// the config writes use.
func (s *Server) forwarderRegistry(c *rux.Context) (*tunnel.ForwarderRegistry, bool) {
	if s.forwarders == nil {
		writeError(c, http.StatusServiceUnavailable, "tunnel forwarder registry unavailable",
			"this server has no forwarder registry wired; start it with `gofer serve`")
		return nil, false
	}
	return s.forwarders, true
}

// forwarderWriteCaller enforces TUN-03's write rule: only a USER caller registers or
// manages a forwarder. A job credential never reaches here (SEC-01's gate refuses every
// unwritten write route first); a worker token is refused here, because a worker is not
// a machine a forwarder runs on and letting it write would blur the two identities.
func (s *Server) forwarderWriteCaller(c *rux.Context) (string, bool) {
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, "worker token cannot manage tunnel forwarders",
			"a worker token may not register, renew or remove a forwarder")
		return "", false
	}
	return callerFromCtx(c), true
}

// writeForwarderRegistryError maps a registry error onto HTTP: an unknown id is a 404
// (the registration expired, or the hub restarted), another caller's id a 403.
func writeForwarderRegistryError(c *rux.Context, err error) {
	switch {
	case errors.Is(err, tunnel.ErrForwarderNotFound):
		writeError(c, http.StatusNotFound, "forwarder not registered",
			"no live registration with that id (it expired, or this server restarted)")
	case errors.Is(err, tunnel.ErrForwarderNotOwner):
		writeError(c, http.StatusForbidden, "forwarder belongs to another caller",
			"a caller may only renew or remove a forwarder it registered")
	default:
		writeError(c, http.StatusInternalServerError, "forwarder registry error", err.Error())
	}
}

// parseForwarderSpecs validates one registration's rule list. A rule that could not be
// forwarded is refused HERE, so the online list can never advertise something the
// forwarder would refuse to run. The CLI sends the specs it already parsed locally, so
// a mismatch means the two ends disagree — better a 400 than a phantom entry.
func parseForwarderSpecs(in []forwarderSpecBody) ([]tunnel.ForwardSpec, error) {
	if len(in) == 0 {
		return nil, errors.New("at least one spec is required")
	}
	out := make([]tunnel.ForwardSpec, 0, len(in))
	for i, s := range in {
		network := s.Network
		if network == "" {
			network = "tcp"
		}
		if network != "tcp" && network != "udp" {
			return nil, fmt.Errorf("spec #%d: network %q is not supported", i+1, s.Network)
		}
		if s.LocalPort < 0 || s.LocalPort > 65535 {
			return nil, fmt.Errorf("spec #%d: invalid local port %d", i+1, s.LocalPort)
		}
		if err := tunnel.ValidateTarget(s.Target); err != nil {
			return nil, fmt.Errorf("spec #%d: %w", i+1, err)
		}
		bind := s.Bind
		if bind == "" {
			bind = "127.0.0.1"
		}
		out = append(out, tunnel.ForwardSpec{Network: network, Bind: bind, LocalPort: s.LocalPort, Target: s.Target})
	}
	return out, nil
}

// buildForwarderViews joins the registrations with the ACTIVE connections the tunnel
// registry holds, on (caller, worker, target): the connection's target is the rule's
// target half, so a forwarder reports exactly the sessions that run through its rules.
//
// Two registrations carrying the same (caller, worker, target) key would each report
// the same sessions — the hub cannot tell which local listener a connection came from,
// and inventing an attribution would be worse than the honest double count. The
// traffic totals stay right because each registration counts the same sessions once.
func (s *Server) buildForwarderViews(regs []tunnel.ForwarderRegistration) []forwarderView {
	active := s.tunnels.List()
	out := make([]forwarderView, 0, len(regs))
	for _, reg := range regs {
		out = append(out, s.forwarderViewOf(reg, active...))
	}
	return out
}

// forwarderViewOf renders one registration; the active tunnels are passed in so a list
// call reads the registry ONCE (rather than once per forwarder).
func (s *Server) forwarderViewOf(reg tunnel.ForwarderRegistration, active ...tunnel.Info) forwarderView {
	v := forwarderView{
		ID:         reg.ID,
		CallerID:   reg.CallerID,
		Worker:     reg.Worker,
		Host:       reg.Host,
		PID:        reg.PID,
		StartedAt:  reg.StartedAt,
		LastSeenAt: reg.LastSeenAt,
		Specs:      make([]forwarderSpecView, 0, len(reg.Specs)),
	}
	for _, sp := range reg.Specs {
		v.Specs = append(v.Specs, forwarderSpecView{Network: sp.Network, Bind: sp.Bind, LocalPort: sp.LocalPort, Target: sp.Target})
	}
	for _, t := range active {
		if t.CallerID != reg.CallerID || t.WorkerID != reg.Worker || !forwarderCarries(reg, t.Target) {
			continue
		}
		v.Connections++
		v.BytesUp += t.BytesUp
		v.BytesDown += t.BytesDown
	}
	return v
}

// forwarderCarries reports whether one of the registration's rules targets the given
// address.
func forwarderCarries(reg tunnel.ForwarderRegistration, target string) bool {
	for _, sp := range reg.Specs {
		if sp.Target == target {
			return true
		}
	}
	return false
}
