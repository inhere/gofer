package config

import (
	"strings"
	"testing"

	yaml "github.com/goccy/go-yaml"
)

// A config that never writes storage.cast.enabled decodes to Enabled=false and
// is not subject to the enabled-only combination checks (G023 opt-in / zero
// regression). A plaintext TTL over the plaintext cap is fine when disabled.
func TestCastDisabledSkipsCombinationCheck(t *testing.T) {
	cfg := &Config{}
	cfg.Storage.Cast.RetentionTTLHours = 48 // > 24 plaintext cap, but disabled
	ApplyDefaults(cfg)
	if cfg.Storage.Cast.Enabled {
		t.Fatal("cast enabled should default to false")
	}
	// Disabled + zero TTL must stay zero (default only applies when enabled).
	cfg2 := &Config{}
	ApplyDefaults(cfg2)
	if cfg2.Storage.Cast.RetentionTTLHours != 0 {
		t.Fatalf("disabled cast TTL = %d, want 0 (no default when disabled)", cfg2.Storage.Cast.RetentionTTLHours)
	}
	if err := validate(cfg); err != nil {
		t.Fatalf("validate should pass for disabled cast with long TTL: %v", err)
	}
}

// Enabled + zero TTL is defaulted to 24h by ApplyDefaults.
func TestCastEnabledDefaultsTTL(t *testing.T) {
	cfg := &Config{}
	cfg.Storage.Cast.Enabled = true
	ApplyDefaults(cfg)
	if cfg.Storage.Cast.RetentionTTLHours != castDefaultTTLHours {
		t.Fatalf("enabled cast TTL = %d, want %d", cfg.Storage.Cast.RetentionTTLHours, castDefaultTTLHours)
	}
}

// Enabled + plaintext (encryption off) + TTL over the plaintext cap fails fast.
func TestCastEnabledPlaintextLongTTLRejected(t *testing.T) {
	cfg := &Config{}
	cfg.Storage.Cast.Enabled = true
	cfg.Storage.Cast.RetentionTTLHours = 48 // > castPlaintextMaxTTLHours (24), encryption off
	ApplyDefaults(cfg)
	err := validate(cfg)
	if err == nil {
		t.Fatal("validate should reject enabled plaintext cast with TTL > plaintext cap")
	}
	if !strings.Contains(err.Error(), "plaintext recording") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Enabled + encryption on but no key_env fails fast (existing check still fires).
func TestCastEnabledEncryptionRequiresKeyEnv(t *testing.T) {
	cfg := &Config{}
	cfg.Storage.Cast.Enabled = true
	cfg.Storage.Cast.Encryption.Enabled = true
	ApplyDefaults(cfg)
	err := validate(cfg)
	if err == nil {
		t.Fatal("validate should reject encryption enabled without key_env")
	}
	if !strings.Contains(err.Error(), "key_env is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Config migration (WEB-03 P3, design §6 待确认): a PRE-P3 config that set
// storage.cast.{encryption,retention_ttl_hours} but never wrote the new `enabled`
// flag decodes to Enabled=false → recording stays OFF (opt-in / zero-regression),
// and its TTL is left as-is (no default applied while disabled). This proves the
// yaml tags map AND that upgrading does not silently start recording.
func TestCastLegacyFixtureNoEnabledStaysOff(t *testing.T) {
	const legacy = `
storage:
  cast:
    retention_ttl_hours: 12
    encryption:
      enabled: true
      key_env: GOFER_CAST_KEY
`
	cfg := &Config{}
	if err := yaml.Unmarshal([]byte(legacy), cfg); err != nil {
		t.Fatalf("decode legacy cast fixture: %v", err)
	}
	ApplyDefaults(cfg)
	if err := validate(cfg); err != nil {
		t.Fatalf("legacy cast fixture should validate: %v", err)
	}
	if cfg.Storage.Cast.Enabled {
		t.Fatal("legacy cast (no `enabled`) must decode to Enabled=false → recording off")
	}
	if cfg.Storage.Cast.RetentionTTLHours != 12 {
		t.Fatalf("disabled cast TTL = %d, want 12 preserved (no default when disabled)", cfg.Storage.Cast.RetentionTTLHours)
	}
}

// The documented storage.cast example block (enabled + encryption + key_env + ttl)
// decodes onto the structs and validates — guards the example yaml field names.
func TestCastEnabledExampleBlockDecodes(t *testing.T) {
	const enabled = `
storage:
  cast:
    enabled: true
    retention_ttl_hours: 24
    encryption:
      enabled: true
      key_env: GOFER_CAST_KEY
`
	cfg := &Config{}
	if err := yaml.Unmarshal([]byte(enabled), cfg); err != nil {
		t.Fatalf("decode enabled cast fixture: %v", err)
	}
	ApplyDefaults(cfg)
	if err := validate(cfg); err != nil {
		t.Fatalf("enabled cast example should validate: %v", err)
	}
	c := cfg.Storage.Cast
	if !c.Enabled || c.RetentionTTLHours != 24 || !c.Encryption.Enabled || c.Encryption.KeyEnv != "GOFER_CAST_KEY" {
		t.Fatalf("cast block = %+v, want enabled + ttl 24 + encryption{enabled,key_env}", c)
	}
}

// Enabled + encryption on + long TTL (> plaintext cap, <= max) is allowed:
// encryption lifts the plaintext cap.
func TestCastEnabledEncryptedLongTTLAllowed(t *testing.T) {
	cfg := &Config{}
	cfg.Storage.Cast.Enabled = true
	cfg.Storage.Cast.RetentionTTLHours = 72 // > 24 plaintext cap, <= 168 max
	cfg.Storage.Cast.Encryption.Enabled = true
	cfg.Storage.Cast.Encryption.KeyEnv = "GOFER_CAST_KEY"
	ApplyDefaults(cfg)
	if err := validate(cfg); err != nil {
		t.Fatalf("validate should allow encrypted cast with long TTL: %v", err)
	}
}
