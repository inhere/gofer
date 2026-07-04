// Package pty is the platform-neutral pty (pseudo terminal) backend for
// interactive jobs (WEB-03 design §6, §13). It abstracts starting a child
// process attached to a pty master so callers get a raw bidirectional byte
// stream + resize + exit-code wait, without importing a platform pty library
// directly.
//
// The interface is implemented per-platform (see pty_unix.go / pty_windows.go):
//
//   - unix: github.com/creack/pty (pty.Start(*exec.Cmd) → ptmx *os.File)
//   - windows: the vendored UserExistsError/conpty (internal/pty/conpty), which
//     takes a single command-line string, so Start adapts (Command,Args) into a
//     safely-quoted command line there.
//
// This makes serve's own local pty a cross-platform drop-in (v0.7): the same
// Pty contract works on a Windows serve and on a Linux worker.
package pty

import (
	"context"
	"io"
)

// Spec is a platform-neutral request to start a child under a pty.
type Spec struct {
	Command string   // executable to run
	Args    []string // args (not including argv[0])
	Env     []string // full process env (KEY=VALUE); nil = inherit os.Environ
	Dir     string   // working directory; "" = inherit
	Cols    int       // initial terminal width (columns); 0 = platform default
	Rows    int       // initial terminal height (rows); 0 = platform default
}

// Pty is a running child attached to a pty master. Read/Write carry the raw byte
// stream to/from the child (stdout+stderr multiplexed on Read, stdin on Write);
// Resize sets the terminal window size; Wait blocks for the child to exit and
// returns its exit code. Close releases the master fd (and, on unix, kills the
// child) — teardown ORDER is owned by the caller's state machine (design §5),
// this type only provides the primitives.
type Pty interface {
	io.ReadWriteCloser
	// Resize sets the pty window size in character cells.
	Resize(cols, rows int) error
	// Wait blocks until the child exits (or ctx is cancelled) and returns the
	// process exit code. A ctx cancellation returns a non-nil error without
	// reaping; the caller kills via Close and calls Wait once.
	Wait(ctx context.Context) (exitCode int, err error)
}

// Start launches spec.Command under a pty and returns the live Pty. Start and
// IsAvailable are provided by the platform files so the common contract above
// stays free of any platform symbol.
//
// (declared per-platform: pty_unix.go / pty_windows.go)

// IsAvailable reports whether a pty backend is usable on this build/host. On
// unix it is always true; on windows it probes the ConPTY API (Win10 1809+).
//
// (declared per-platform: pty_unix.go / pty_windows.go)
