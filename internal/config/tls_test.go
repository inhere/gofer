package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTLSConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gofer.yaml")
	if err := os.WriteFile(path, []byte("server:\n  addr: 127.0.0.1:8765\n  tls:\n    addr: 127.0.0.1:9443\n    cert_file: server.crt\n    key_file: server.key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.TLS == nil || cfg.Server.TLS.Addr != "127.0.0.1:9443" || cfg.Server.TLS.CertFile != "server.crt" || cfg.Server.TLS.KeyFile != "server.key" {
		t.Fatalf("TLS config = %#v", cfg.Server.TLS)
	}
}
