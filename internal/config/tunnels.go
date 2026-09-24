package config

import (
	"fmt"
	yaml "github.com/goccy/go-yaml"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/tunnel"
)

// TunnelProfile is one saved `gofer tunnel forward` invocation: which worker to
// reach and which ports to forward through it.
type TunnelProfile struct {
	Worker string   `yaml:"worker"`
	Specs  []string `yaml:"specs"`
	Note   string   `yaml:"note,omitempty"`
}

// ServerTunnelConfig is the server.tunnel block (TUN-03): the hub-side policy of the
// tunnel VISIBILITY surface. Today it holds one knob — how long a forwarder
// registration without a heartbeat stays listed.
type ServerTunnelConfig struct {
	// ForwarderTTLSec is the lifetime of a `gofer tun forward` registration measured
	// from its last heartbeat. 0/unset => tunnel.DefaultForwarderTTL (90s), which is
	// three heartbeat intervals. Read per request, so a hot edit applies to the next
	// registration read or write.
	ForwarderTTLSec int `yaml:"forwarder_ttl_sec,omitempty"`
}

// EffectiveForwarderTTL resolves the registered lifetime: the configured seconds, else
// tunnel.DefaultForwarderTTL. A negative or zero value is the default, never "expire
// immediately" — a value that would erase every live forwarder is not a useful knob.
func (t ServerTunnelConfig) EffectiveForwarderTTL() time.Duration {
	if t.ForwarderTTLSec > 0 {
		return time.Duration(t.ForwarderTTLSec) * time.Second
	}
	return tunnel.DefaultForwarderTTL
}

// NormalizeTunnelProfile returns p with its rule list flattened by tunnel.SplitSpecs:
// the comma form is a way to WRITE several rules, never a rule of its own, so it must
// not reach the local file, the server table or a `tun forward -n` run. Use this
// before validating a profile that came from a user.
func NormalizeTunnelProfile(p TunnelProfile) (TunnelProfile, error) {
	specs, err := tunnel.SplitSpecs(p.Specs)
	if err != nil {
		return p, err
	}
	p.Specs = specs
	return p, nil
}

// Tunnels is the whole preset file.
//
// It deliberately lives in its OWN user-level file (<config-dir>/tunnels.yaml,
// alongside worker.yaml) rather than inside config.yaml: Save() re-emits every
// managed top-level key wholesale, so writing a preset through it would risk
// freezing runtime overlays and one-off CLI flags into the user's config, and
// Config.Clone would have to be widened first (D-MED-7).
type Tunnels struct {
	Forwards map[string]TunnelProfile `yaml:"forwards"`
}

// LoadTunnels reads the preset file. A missing file is an empty set, not an error:
// presets are optional and the CLI must work on a machine that never saved one.
//
// DEPRECATED(v0.60.2): remove in v0.63 — TUN-03 moved the source of truth to the server
// (`gofer tun presets`, the tunnel_presets table); the local file is now read only as a
// fallback for a preset the server does not have, and as the source `gofer tun presets
// push` uploads. New code reads the server.
func LoadTunnels() (*Tunnels, error) {
	p, err := UserTunnelsPath()
	if err != nil {
		return nil, err
	}
	out := &Tunnels{Forwards: map[string]TunnelProfile{}}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, out); err != nil {
		return nil, fmt.Errorf("decode tunnels %s: %w", p, err)
	}
	if out.Forwards == nil {
		out.Forwards = map[string]TunnelProfile{}
	}
	return out, nil
}

// SaveTunnels writes the preset file 0600: a preset names internal hosts and the
// worker that can reach them.
func SaveTunnels(t *Tunnels) error {
	p, err := UserTunnelsPath()
	if err != nil {
		return err
	}
	if t == nil {
		t = &Tunnels{}
	}
	if t.Forwards == nil {
		t.Forwards = map[string]TunnelProfile{}
	}
	b, err := yaml.Marshal(t)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// ValidateTunnelProfile rejects a preset that could not be run. Validation lives
// here rather than in the CLI so every caller gets it and a bad preset can never
// reach the file.
//
// The rule list may arrive comma-joined (TUN-04): validation sees the SPLIT list, so a
// profile is judged by what it will actually run. A malformed rule is reported with its
// position and text (`spec #2 "not-a-spec"`), not as a bare "invalid spec".
func ValidateTunnelProfile(name string, p TunnelProfile) error {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\\/ \t\r\n") {
		return fmt.Errorf("invalid tunnel preset name %q: must be non-empty and contain no whitespace or path separator", name)
	}
	if strings.TrimSpace(p.Worker) == "" {
		return fmt.Errorf("tunnel preset %q: worker is required", name)
	}
	specs, err := tunnel.SplitSpecs(p.Specs)
	if err != nil {
		return fmt.Errorf("tunnel preset %q: at least one forward spec is required", name)
	}
	if _, err := tunnel.ParseSpecs(specs); err != nil {
		return fmt.Errorf("tunnel preset %q: %w", name, err)
	}
	return nil
}

// UpsertTunnel stores a preset, refusing to replace an existing one unless force. The
// stored rule list is the SPLIT one (design §三): a preset is a set of rules, and the
// comma form is only a way to write it.
func UpsertTunnel(name string, profile TunnelProfile, force bool) error {
	flat, err := NormalizeTunnelProfile(profile)
	if err != nil {
		// Keep the validation wording for the one case splitting can fail on (nothing
		// usable in the list) — callers have always seen it from ValidateTunnelProfile.
		if verr := ValidateTunnelProfile(name, profile); verr != nil {
			return verr
		}
		return err
	}
	if err := ValidateTunnelProfile(name, flat); err != nil {
		return err
	}
	t, err := LoadTunnels()
	if err != nil {
		return err
	}
	if _, ok := t.Forwards[name]; ok && !force {
		return fmt.Errorf("tunnel preset %q already exists (use --force to overwrite)", name)
	}
	t.Forwards[name] = flat
	return SaveTunnels(t)
}

// DeleteTunnel removes a preset, reporting a name that was never saved.
func DeleteTunnel(name string) error {
	t, err := LoadTunnels()
	if err != nil {
		return err
	}
	if _, ok := t.Forwards[name]; !ok {
		return fmt.Errorf("tunnel preset %q not found", name)
	}
	delete(t.Forwards, name)
	return SaveTunnels(t)
}
