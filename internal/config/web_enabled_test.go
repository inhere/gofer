package config

import (
	"path/filepath"
	"testing"
)

// TestWebEnabledDefaultsTrue verifies an unset web_enabled defaults to true
// after Load/applyDefaults.
func TestWebEnabledDefaultsTrue(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	write(t, p, `
projects:
  demo:
    host_path: /tmp/demo
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.WebEnabled == nil {
		t.Fatal("WebEnabled should be defaulted (non-nil) after Load")
	}
	if !cfg.Server.IsWebEnabled() {
		t.Fatal("unset web_enabled should default to true")
	}
}

// TestWebEnabledExplicitFalse verifies web_enabled:false disables the console.
func TestWebEnabledExplicitFalse(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	write(t, p, `
server:
  web_enabled: false
projects:
  demo:
    host_path: /tmp/demo
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.IsWebEnabled() {
		t.Fatal("explicit web_enabled:false should disable the web console")
	}
}

// TestIsWebEnabledZeroValue verifies a bare ServerConfig (nil pointer) reports
// enabled, matching the default-on semantics.
func TestIsWebEnabledZeroValue(t *testing.T) {
	if !(ServerConfig{}).IsWebEnabled() {
		t.Fatal("zero-value ServerConfig should report web enabled (nil => true)")
	}
	f := false
	if (ServerConfig{WebEnabled: &f}).IsWebEnabled() {
		t.Fatal("WebEnabled=&false should report disabled")
	}
}
