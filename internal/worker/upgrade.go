package worker

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/util"
)

// ErrUpgradeDirNotWritable means the running binary cannot be replaced by this user.
var ErrUpgradeDirNotWritable = errors.New("upgrade directory is not writable")

// UpgradeCapability reports whether this worker can replace its running binary.
type UpgradeCapability struct {
	Capable bool
	Error   string
}

// CheckUpgradeCapability probes the executable directory without changing the
// running binary. It is safe to call while jobs are running.
func CheckUpgradeCapability(exe string) UpgradeCapability {
	if exe == "" {
		exe = runningBinaryPath()
	}
	dir := filepath.Dir(exe)
	f, err := os.CreateTemp(dir, ".gofer.upgrade-check-*")
	if err == nil {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		return UpgradeCapability{Capable: true}
	}
	return UpgradeCapability{Error: i18n.T(i18n.Default, "cli.worker.upgrade.dir_not_writable", map[string]any{
		"dir": dir, "user": currentUpgradeUser(), "owner": upgradeDirOwner(dir),
	})}
}

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
