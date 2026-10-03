package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// VerifyUpgradeFile checks the worker-side download before it can replace the
// running binary. Size and digest are both mandatory protocol facts.
func VerifyUpgradeFile(path string, size int64, wantSHA256 string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("upgrade stat: %w", err)
	}
	if !st.Mode().IsRegular() {
		return errors.New("upgrade payload is not a regular file")
	}
	if size < 0 || st.Size() != size {
		return fmt.Errorf("upgrade size mismatch: got %d want %d", st.Size(), size)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("upgrade open: %w", err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		_ = f.Close()
		return fmt.Errorf("upgrade hash: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("upgrade close: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !equalHex(got, wantSHA256) {
		return fmt.Errorf("upgrade checksum mismatch: got %s want %s", got, wantSHA256)
	}
	return nil
}

func equalHex(a, b string) bool {
	return len(a) == len(b) && subtleConstantTimeEqual([]byte(a), []byte(b))
}

func subtleConstantTimeEqual(a, b []byte) bool {
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// SwitchBinary atomically puts candidate at target and leaves one rollback copy
// at target.old. On Unix rename is atomic; on Windows a running executable may be
// renamed, so the same sequence works without stopping the old process first.
func SwitchBinary(target, candidate string) (rollback func() error, err error) {
	if target == "" || candidate == "" {
		return nil, errors.New("upgrade target and candidate are required")
	}
	old := target + ".old"
	_ = os.Remove(old)
	if err := os.Rename(target, old); err != nil {
		return nil, fmt.Errorf("upgrade park old binary: %w", err)
	}
	if err := os.Rename(candidate, target); err != nil {
		_ = os.Rename(old, target)
		return nil, fmt.Errorf("upgrade install new binary: %w", err)
	}
	rolledBack := false
	return func() error {
		if rolledBack {
			return nil
		}
		rolledBack = true
		_ = os.Remove(target)
		if err := os.Rename(old, target); err != nil {
			return fmt.Errorf("upgrade restore old binary: %w", err)
		}
		return nil
	}, nil
}

// upgradeTempPath is where the candidate binary is downloaded: beside the running
// executable (same filesystem, so the final rename is atomic) and keeping its
// extension (Windows only runs a candidate it can recognise as an executable).
func upgradeTempPath(target string) string {
	ext := filepath.Ext(target)
	base := strings.TrimSuffix(filepath.Base(target), ext)
	return filepath.Join(filepath.Dir(target), "."+base+".upgrade"+ext)
}

func runningBinaryPath() string {
	path, _ := os.Executable()
	if runtime.GOOS == "windows" && filepath.Ext(path) == "" {
		path += ".exe"
	}
	return path
}
