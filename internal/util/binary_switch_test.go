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
