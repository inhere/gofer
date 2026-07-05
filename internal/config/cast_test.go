package config

import (
	"strings"
	"testing"
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
