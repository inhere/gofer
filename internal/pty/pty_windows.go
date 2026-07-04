//go:build windows

package pty

import (
	"context"
	"strings"

	"github.com/inhere/gofer/internal/pty/conpty"
	"golang.org/x/sys/windows"
)

// IsAvailable probes the ConPTY API (Win10 1809+ / kernel32 pseudo-console procs).
func IsAvailable() bool { return conpty.IsConPtyAvailable() }

// Start runs spec under a ConPTY. Unlike creack (which takes an *exec.Cmd), the
// conpty API takes ONE command-line string, so (Command,Args) is joined into a
// safely-quoted command line via windows.EscapeArg (no shell involved).
func Start(spec Spec) (Pty, error) {
	opts := make([]conpty.ConPtyOption, 0, 3)
	if spec.Cols > 0 && spec.Rows > 0 {
		opts = append(opts, conpty.ConPtyDimensions(spec.Cols, spec.Rows))
	}
	if spec.Dir != "" {
		opts = append(opts, conpty.ConPtyWorkDir(spec.Dir))
	}
	if len(spec.Env) > 0 {
		opts = append(opts, conpty.ConPtyEnv(spec.Env))
	}
	c, err := conpty.Start(buildCommandLine(spec.Command, spec.Args), opts...)
	if err != nil {
		return nil, err
	}
	return &winPty{c: c}, nil
}

// winPty adapts *conpty.ConPty to the Pty interface.
type winPty struct {
	c *conpty.ConPty
}

func (p *winPty) Read(b []byte) (int, error)  { return p.c.Read(b) }
func (p *winPty) Write(b []byte) (int, error) { return p.c.Write(b) }
func (p *winPty) Resize(cols, rows int) error { return p.c.Resize(cols, rows) }
func (p *winPty) Close() error                { return p.c.Close() }

func (p *winPty) Wait(ctx context.Context) (int, error) {
	code, err := p.c.Wait(ctx)
	return int(code), err
}

// buildCommandLine joins the command + args into a single Windows command line,
// quoting each token per the CommandLineToArgvW rules (windows.EscapeArg) so a
// space/quote in an arg cannot inject an extra token.
func buildCommandLine(command string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, windows.EscapeArg(command))
	for _, a := range args {
		parts = append(parts, windows.EscapeArg(a))
	}
	return strings.Join(parts, " ")
}
