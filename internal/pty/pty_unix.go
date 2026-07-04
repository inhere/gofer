//go:build unix

package pty

import (
	"context"
	"errors"
	"os"
	"os/exec"

	creack "github.com/creack/pty"
)

// IsAvailable is always true on unix: creack/pty opens /dev/ptmx at Start time
// and a failure surfaces there (probing here would just duplicate that).
func IsAvailable() bool { return true }

// Start runs spec under a freshly allocated pty (unix: creack/pty).
func Start(spec Spec) (Pty, error) {
	cmd := exec.Command(spec.Command, spec.Args...)
	if spec.Dir != "" {
		cmd.Dir = spec.Dir
	}
	if len(spec.Env) > 0 {
		cmd.Env = spec.Env // nil env => inherit os.Environ (exec default)
	}

	var (
		ptmx *os.File
		err  error
	)
	if spec.Cols > 0 && spec.Rows > 0 {
		// Set the window size BEFORE exec so a size-sensitive program reads the
		// right dimensions from its first tcgetwinsize.
		ptmx, err = creack.StartWithSize(cmd, &creack.Winsize{
			Rows: uint16(spec.Rows), Cols: uint16(spec.Cols),
		})
	} else {
		ptmx, err = creack.Start(cmd)
	}
	if err != nil {
		return nil, err
	}
	return &unixPty{ptmx: ptmx, cmd: cmd}, nil
}

// unixPty wraps the pty master fd + the child cmd.
type unixPty struct {
	ptmx *os.File
	cmd  *exec.Cmd
}

func (p *unixPty) Read(b []byte) (int, error)  { return p.ptmx.Read(b) }
func (p *unixPty) Write(b []byte) (int, error) { return p.ptmx.Write(b) }

func (p *unixPty) Resize(cols, rows int) error {
	return creack.Setsize(p.ptmx, &creack.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

// Close closes the master fd and kills the child. It is the "close master +
// kill child" primitive; the caller's state machine decides WHEN to call it and
// then reaps via Wait exactly once.
func (p *unixPty) Close() error {
	cerr := p.ptmx.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	return cerr
}

// Wait reaps the child (cmd.Wait) and returns its exit code. It must be called
// exactly once. A REAL process exit — even a non-zero or signalled one — returns
// (code, nil): the exit code carries the outcome (parity with the windows conpty
// backend and with local.Runner's "non-zero exit is not a runner error"). Only a
// genuine reap failure returns a non-nil error. A ctx cancellation returns early
// with ctx.Err() and does NOT reap; the caller kills via Close and lets the inner
// reap complete. The state machine passes context.Background here, so it gets a
// plain blocking reap.
func (p *unixPty) Wait(ctx context.Context) (int, error) {
	type res struct {
		code int
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		werr := p.cmd.Wait()
		if werr == nil {
			ch <- res{code: 0}
			return
		}
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			ch <- res{code: ee.ExitCode()} // clean process exit (maybe non-zero); no error
			return
		}
		ch <- res{code: -1, err: werr} // genuine reap failure
	}()
	select {
	case <-ctx.Done():
		return -1, ctx.Err()
	case r := <-ch:
		return r.code, r.err
	}
}
