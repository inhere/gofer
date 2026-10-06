package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvRoots adds directories (an OS path list) a transcript may live under, besides the
// user's home directory. Operators who keep agent session stores elsewhere (a
// relocated CLAUDE_CONFIG_DIR / CODEX_HOME is picked up automatically) set it.
const EnvRoots = "GOFER_TRANSCRIPT_ROOTS"

// MaxTailBytes is the hard cap of one safe tail read (the wire cap of a worker answer).
const MaxTailBytes = 512 * 1024

// Roots lists where a transcript file may live.
func Roots() []string {
	var roots []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots, home)
	}
	for _, env := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			roots = append(roots, v)
		}
	}
	if v := os.Getenv(EnvRoots); v != "" {
		for _, p := range filepath.SplitList(v) {
			if p = strings.TrimSpace(p); p != "" {
				roots = append(roots, p)
			}
		}
	}
	return roots
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// CheckPath is the boundary for reading a session-registered transcript: an absolute
// .jsonl path that, with symlinks resolved, is a regular file under an allowed root.
// The server only ever passes the path a session registered, but the reader must not
// trust that — it would otherwise be a remote file reader. It returns the resolved path.
func CheckPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("transcript path must be absolute")
	}
	path = filepath.Clean(path)
	if !strings.EqualFold(filepath.Ext(path), ".jsonl") {
		return "", fmt.Errorf("transcript path must be a .jsonl file")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("transcript not readable: %w", err)
	}
	ok := false
	for _, r := range Roots() {
		rr := r
		if e, err := filepath.EvalSymlinks(r); err == nil {
			rr = e
		}
		if within(rr, real) {
			ok = true
			break
		}
	}
	if !ok {
		return "", fmt.Errorf("transcript path is outside the allowed roots")
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("transcript not readable: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("transcript is not a regular file")
	}
	return real, nil
}

// ReadTailSafe vets the path (CheckPath) and reads at most maxBytes from its end (capped
// at MaxTailBytes; <=0 = DefaultTailBytes). size is the file's full size and truncated
// says the file was longer than what came back.
func ReadTailSafe(path string, maxBytes int64) (data []byte, size int64, truncated bool, err error) {
	real, err := CheckPath(path)
	if err != nil {
		return nil, 0, false, err
	}
	if maxBytes <= 0 {
		maxBytes = DefaultTailBytes
	}
	if maxBytes > MaxTailBytes {
		maxBytes = MaxTailBytes
	}
	fi, err := os.Stat(real)
	if err != nil {
		return nil, 0, false, err
	}
	data, err = ReadTail(real, maxBytes)
	if err != nil {
		return nil, 0, false, err
	}
	return data, fi.Size(), fi.Size() > int64(len(data)), nil
}
