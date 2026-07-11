package testcmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// Path returns an absolute path to the gofer test helper binary.
func Path(t testing.TB) string {
	t.Helper()
	buildOnce.Do(func() {
		_, file, _, ok := runtime.Caller(0)
		if !ok {
			buildErr = os.ErrInvalid
			return
		}
		root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
		outDir := filepath.Join(root, "tmp", "test-bin")
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			buildErr = err
			return
		}
		// Output name carries the PID so concurrent `go test ./...` package
		// processes each build to a distinct file instead of racing the same
		// path (text file busy). The shared build cache stays concurrency-safe;
		// only the -o target must be unique per process.
		name := fmt.Sprintf("gofer-testcmd-%d", os.Getpid())
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		binPath = filepath.Join(outDir, name)
		cmd := exec.Command("go", "build", "-o", binPath, "./internal/testutil/testcmd/testcmd")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = &buildError{err: err, out: string(out)}
		}
	})
	if buildErr != nil {
		t.Fatalf("build testcmd: %v", buildErr)
	}
	return binPath
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
