package config

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// A base64-encoded 32-byte key resolves to the exact decoded bytes.
func TestResolveCastSecretBase64(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	t.Setenv("GOFER_TEST_CAST_KEY", base64.StdEncoding.EncodeToString(raw))
	c := CastConfig{Enabled: true, Encryption: CastEncryptionConfig{Enabled: true, KeyEnv: "GOFER_TEST_CAST_KEY"}}
	got, err := c.ResolveCastSecret()
	if err != nil {
		t.Fatalf("ResolveCastSecret: %v", err)
	}
	if string(got) != string(raw) {
		t.Fatalf("decoded bytes mismatch (len=%d)", len(got))
	}
}

// A hex-encoded 32-byte key (64 hex chars) resolves via the hex path.
func TestResolveCastSecretHex(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(200 - i)
	}
	t.Setenv("GOFER_TEST_CAST_KEY", hex.EncodeToString(raw))
	c := CastConfig{Encryption: CastEncryptionConfig{KeyEnv: "GOFER_TEST_CAST_KEY"}}
	got, err := c.ResolveCastSecret()
	if err != nil {
		t.Fatalf("ResolveCastSecret hex: %v", err)
	}
	if string(got) != string(raw) {
		t.Fatalf("hex decoded bytes mismatch (len=%d)", len(got))
	}
}

// An empty/unset env var fails fast (missing key).
func TestResolveCastSecretMissingEnv(t *testing.T) {
	t.Setenv("GOFER_TEST_CAST_KEY", "")
	c := CastConfig{Encryption: CastEncryptionConfig{KeyEnv: "GOFER_TEST_CAST_KEY"}}
	if _, err := c.ResolveCastSecret(); err == nil {
		t.Fatal("expected error for empty key env value")
	}
}

// An empty key_env NAME fails fast.
func TestResolveCastSecretEmptyKeyEnvName(t *testing.T) {
	c := CastConfig{Encryption: CastEncryptionConfig{KeyEnv: ""}}
	if _, err := c.ResolveCastSecret(); err == nil {
		t.Fatal("expected error for empty key_env name")
	}
}

// A validly-encoded but too-short (< 32 bytes) key is rejected, and the error must
// not leak the key material (SR403): assert only the length hint is present.
func TestResolveCastSecretTooShort(t *testing.T) {
	raw := make([]byte, 16) // 128-bit, below the 256-bit floor
	enc := base64.StdEncoding.EncodeToString(raw)
	t.Setenv("GOFER_TEST_CAST_KEY", enc)
	c := CastConfig{Encryption: CastEncryptionConfig{KeyEnv: "GOFER_TEST_CAST_KEY"}}
	_, err := c.ResolveCastSecret()
	if err == nil {
		t.Fatal("expected error for short key")
	}
	if !strings.Contains(err.Error(), "need >= 32") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), enc) {
		t.Fatalf("error text leaked the key material: %v", err)
	}
}

// A value that is neither valid base64 nor hex fails fast.
func TestResolveCastSecretBadEncoding(t *testing.T) {
	t.Setenv("GOFER_TEST_CAST_KEY", "not valid base64!!! nor hex ***")
	c := CastConfig{Encryption: CastEncryptionConfig{KeyEnv: "GOFER_TEST_CAST_KEY"}}
	if _, err := c.ResolveCastSecret(); err == nil {
		t.Fatal("expected error for undecodable key")
	}
}
