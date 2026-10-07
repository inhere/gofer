package worker

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/inhere/gofer/internal/util"
)

// VerifyUpgradeFile checks the worker-side download before it can replace the
// running binary. Size and digest are both mandatory protocol facts.
func VerifyUpgradeFile(path string, size int64, wantSHA256 string) error {
	return util.VerifyUpgradeFile(path, size, wantSHA256)
}

// SwitchBinary atomically puts candidate at target and leaves one rollback copy
// at target.old. On Unix rename is atomic; on Windows a running executable may be
// renamed, so the same sequence works without stopping the old process first.
func SwitchBinary(target, candidate string) (rollback func() error, err error) {
	return util.SwitchBinary(target, candidate)
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
