package testcmd

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// Path returns an absolute path to the gofer test helper binary.
//
// The helper is built ONCE per checkout, not once per test process: it lives at a
// fixed path under the repo's tmp/, guarded by a lock file, and is rebuilt only
// when a Go source in the checkout (or go.mod/go.sum) is newer than it. Every
// package in a `go test ./...` run therefore reuses one file, where each package
// used to link its own copy — on the Windows CI runner a fresh .exe is also a
// fresh Defender scan, so the redundant builds were paid twice over.
func Path(t testing.TB) string {
	t.Helper()
	buildOnce.Do(func() { binPath, buildErr = buildHelper() })
	if buildErr != nil {
		t.Fatalf("build testcmd: %v", buildErr)
	}
	return binPath
}

// lockStale is how old a build lock may get before a waiter assumes its owner
// died (a killed test process leaves the file behind) and reclaims it.
const lockStale = 5 * time.Minute

// buildHelper builds (or reuses) the helper binary and returns its path.
func buildHelper() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", os.ErrInvalid
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	outDir := filepath.Join(root, "tmp", "test-bin")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	name := "gofer-testcmd"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	shared := filepath.Join(outDir, name)

	unlock, err := lockBuild(shared + ".lock")
	if err != nil {
		return "", err
	}
	defer unlock()
	if helperFresh(shared, root) {
		return shared, nil
	}
	// Build beside the target and move it into place: a concurrent reader never
	// sees a half-written binary. When the move fails the old file is in use
	// (Windows cannot replace a running .exe), so this process uses its own copy
	// instead of fighting it.
	tmp := fmt.Sprintf("%s.%d.tmp", shared, os.Getpid())
	cmd := exec.Command("go", "build", "-o", tmp, "./internal/testutil/testcmd/testcmd")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", &buildError{err: err, out: string(out)}
	}
	if err := os.Rename(tmp, shared); err != nil {
		return tmp, nil
	}
	return shared, nil
}

// helperFresh reports whether bin is newer than every Go source in the checkout
// (and go.mod/go.sum), i.e. whether rebuilding it would be a no-op. tmp/ is
// skipped: that is where the helper itself and other test scratch live.
func helperFresh(bin, root string) bool {
	fi, err := os.Stat(bin)
	if err != nil {
		return false
	}
	built := fi.ModTime()
	stale := false
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: not a reason to rebuild
		}
		if d.IsDir() {
			switch d.Name() {
			case "tmp", ".git", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") && d.Name() != "go.mod" && d.Name() != "go.sum" {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(built) {
			stale = true
			return fs.SkipAll
		}
		return nil
	})
	return !stale
}

// lockBuild takes the exclusive build lock, waiting for a concurrent build to
// finish. The returned func releases it.
func lockBuild(path string) (func(), error) {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if fi, serr := os.Stat(path); serr == nil && time.Since(fi.ModTime()) > lockStale {
			_ = os.Remove(path) // owner is gone
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("testcmd: build lock %s not acquired in time", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func Cmd(t testing.TB, args ...string) []string {
	t.Helper()
	return append([]string{Path(t)}, args...)
}

type buildError struct {
	err error
	out string
}

func (e *buildError) Error() string {
	if e.out == "" {
		return e.err.Error()
	}
	return e.err.Error() + "\n" + e.out
}
