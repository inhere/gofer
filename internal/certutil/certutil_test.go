package certutil

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGenerateReusesCAAndWritesSANCertificate(t *testing.T) {
	dir := t.TempDir()
	first, err := Generate(dir, []string{"127.0.0.1", "gofer.test"})
	if err != nil {
		t.Fatal(err)
	}
	caBefore, err := os.ReadFile(first.CACertFile)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate(dir, []string{"192.0.2.10"})
	if err != nil {
		t.Fatal(err)
	}
	caAfter, err := os.ReadFile(second.CACertFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(caBefore) != string(caAfter) {
		t.Fatal("existing CA was replaced")
	}
	cert, err := os.ReadFile(filepath.Clean(second.ServerCertFile))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(cert)
	if block == nil {
		t.Fatal("server certificate is not PEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.IPAddresses[0].String() != "192.0.2.10" {
		t.Fatalf("SAN IPs = %v", leaf.IPAddresses)
	}
	if runtime.GOOS != "windows" {
		if mode := fileMode(second.ServerKeyFile); mode.Perm() != 0o600 {
			t.Fatalf("server key mode = %o, want 600", mode.Perm())
		}
	}
}

func fileMode(path string) os.FileMode {
	info, _ := os.Stat(path)
	return info.Mode()
}
