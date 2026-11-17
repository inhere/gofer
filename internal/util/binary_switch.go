// Package util contains the file-only part of Gofer binary upgrades.
// Worker handover and managed server lifecycle remain with their own owners.
package util

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
		f.Close()
		return fmt.Errorf("upgrade hash: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("upgrade close: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, wantSHA256) {
		return fmt.Errorf("upgrade checksum mismatch: got %s want %s", got, wantSHA256)
	}
	return nil
}

// Switch parks the old image beside target, installs a same-volume candidate,
// and returns a rollback closure. The old image remains until caller cleanup.
func SwitchBinary(target, candidate string) (rollback func() error, err error) {
	if target == "" || candidate == "" {
		return nil, errors.New("upgrade target and candidate are required")
	}
	old := parkPath(target)
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

// parkPath picks where SwitchBinary parks the running image. It first removes the
// images earlier upgrades parked (target.old and target.old-<n>). One of them can
// still be the image of a long-lived process that outlived an upgrade (a tunnel,
// say); Windows refuses to delete or overwrite it, so the old image then goes to a
// fresh target.old-<n> and the stuck one is removed by a later upgrade.
func parkPath(target string) string {
	old := target + ".old"
	dir, base := filepath.Dir(target), filepath.Base(old)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if name := e.Name(); name == base || strings.HasPrefix(name, base+"-") {
				_ = os.Remove(filepath.Join(dir, name))
			}
		}
	}
	if _, err := os.Lstat(old); errors.Is(err, os.ErrNotExist) {
		return old
	}
	for n := time.Now().UnixNano(); ; n++ {
		alt := fmt.Sprintf("%s-%d", old, n)
		if _, err := os.Lstat(alt); errors.Is(err, os.ErrNotExist) {
			return alt
		}
	}
}

// Stage copies a verified candidate into the executable's directory, so the
// final rename stays on one volume. The destination is unique to upgradeID.
func StageUpgradeFile(source, target, upgradeID string, size int64, wantSHA256 string) (string, error) {
	if err := VerifyUpgradeFile(source, size, wantSHA256); err != nil {
		return "", err
	}
	ext := filepath.Ext(target)
	path := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+"."+upgradeID+".candidate"+ext)
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(path)
		return "", err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(path)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	if err := VerifyUpgradeFile(path, size, wantSHA256); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}
