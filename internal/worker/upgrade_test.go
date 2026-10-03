package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkerUpgradeVerifiesChecksum(t *testing.T) {
	p := filepath.Join(t.TempDir(), "worker.new")
	data := []byte("candidate")
	if err := os.WriteFile(p, data, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if err := VerifyUpgradeFile(p, int64(len(data)), hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	if err := VerifyUpgradeFile(p, int64(len(data)), "deadbeef"); err == nil {
		t.Fatal("bad checksum accepted")
	}
}

func TestWorkerUpgradeRollsBackWhenNewFailsToRegister(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "worker.exe")
	candidate := filepath.Join(dir, "worker.new")
	if err := os.WriteFile(target, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("new"), 0o700); err != nil {
		t.Fatal(err)
	}
	rollback, err := SwitchBinary(target, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "old" {
		t.Fatalf("rollback target=%q err=%v", got, err)
	}
}
