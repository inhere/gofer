package config

import (
	"fmt"
	yaml "github.com/goccy/go-yaml"
	"os"
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/internal/tunnel"
)

// TunnelProfile is one saved `gofer tunnel forward` invocation: which worker to
// reach and which ports to forward through it.
type TunnelProfile struct {
	Worker string   `yaml:"worker"`
	Specs  []string `yaml:"specs"`
	Note   string   `yaml:"note,omitempty"`
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
func ValidateTunnelProfile(name string, p TunnelProfile) error {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\\/ \t\r\n") {
		return fmt.Errorf("invalid tunnel preset name %q: must be non-empty and contain no whitespace or path separator", name)
	}
	if strings.TrimSpace(p.Worker) == "" {
		return fmt.Errorf("tunnel preset %q: worker is required", name)
	}
	if len(p.Specs) == 0 {
		return fmt.Errorf("tunnel preset %q: at least one forward spec is required", name)
	}
	for _, s := range p.Specs {
		if _, err := tunnel.ParseForwardSpec(s); err != nil {
			return fmt.Errorf("tunnel preset %q: %w", name, err)
		}
	}
	return nil
}

// UpsertTunnel stores a preset, refusing to replace an existing one unless force.
func UpsertTunnel(name string, profile TunnelProfile, force bool) error {
	if err := ValidateTunnelProfile(name, profile); err != nil {
		return err
	}
	t, err := LoadTunnels()
	if err != nil {
		return err
	}
	if _, ok := t.Forwards[name]; ok && !force {
		return fmt.Errorf("tunnel preset %q already exists (use --force to overwrite)", name)
	}
	t.Forwards[name] = profile
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
