package util

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestStageAndSwitchBinaryRollback(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "managed.exe")
	source := filepath.Join(dir, "candidate.exe")
	assert.Require(t, assert.NoErr(t, os.WriteFile(target, []byte("old"), 0o700)))
	newBytes := []byte("new candidate")
	assert.Require(t, assert.NoErr(t, os.WriteFile(source, newBytes, 0o700)))
	hash := sha256.Sum256(newBytes)
	staged, err := StageUpgradeFile(source, target, "abc", int64(len(newBytes)), hex.EncodeToString(hash[:]))
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, filepath.Dir(target), filepath.Dir(staged))
	assert.Require(t, assert.NoErr(t, VerifyUpgradeFile(staged, int64(len(newBytes)), hex.EncodeToString(hash[:]))))
	rollback, err := SwitchBinary(target, staged)
	assert.Require(t, assert.NoErr(t, err))
	installed, err := os.ReadFile(target)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, string(newBytes), string(installed))
	assert.Require(t, assert.NoErr(t, rollback()))
	restored, err := os.ReadFile(target)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "old", string(restored))
}

func TestStageRejectsWrongHashWithoutCreatingCandidate(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "candidate.exe")
	assert.Require(t, assert.NoErr(t, os.WriteFile(source, []byte("new"), 0o700)))
	_, err := StageUpgradeFile(source, filepath.Join(dir, "managed.exe"), "abc", 3, "bad")
	assert.Err(t, err)
	if matches, globErr := filepath.Glob(filepath.Join(dir, "*.candidate.exe")); globErr != nil || len(matches) != 0 {
		t.Fatalf("unverified staged binary remained: %v, %v", matches, globErr)
	}
}

// TestSwitchBinaryParksBesideAStuckOldImage: an earlier parked image that cannot be
// removed (on Windows: still the image of a long-lived process) no longer blocks the
// upgrade; the running image is parked under a fresh name, rollback restores it from
// there, and stale parked images that can be removed are cleaned up.
func TestSwitchBinaryParksBesideAStuckOldImage(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "managed.exe")
	assert.Require(t, assert.NoErr(t, os.WriteFile(target, []byte("old"), 0o700)))
	// A non-empty directory cannot be removed or replaced by a rename on any platform,
	// which stands in for the file a running process holds.
	stuck := target + ".old"
	assert.Require(t, assert.NoErr(t, os.MkdirAll(filepath.Join(stuck, "held"), 0o700)))
	stale := target + ".old-1"
	assert.Require(t, assert.NoErr(t, os.WriteFile(stale, []byte("stale"), 0o700)))
	candidate := filepath.Join(dir, ".managed.exe.abc.candidate.exe")
	assert.Require(t, assert.NoErr(t, os.WriteFile(candidate, []byte("new"), 0o700)))

	rollback, err := SwitchBinary(target, candidate)
	assert.Require(t, assert.NoErr(t, err))
	installed, err := os.ReadFile(target)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "new", string(installed))
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale parked image still there: %v", err)
	}
	parked, _ := filepath.Glob(filepath.Join(dir, "managed.exe.old-*"))
	if len(parked) != 1 {
		t.Fatalf("parked images = %v, want one fresh target.old-<n>", parked)
	}

	assert.Require(t, assert.NoErr(t, rollback()))
	restored, err := os.ReadFile(target)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "old", string(restored))
}
