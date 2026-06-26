//go:build windows

package daemon

import (
	"errors"
	"os/exec"
)

// errNotSupported is returned for every daemon operation on Windows: gofer's
// background mode targets Linux (container / WSL). On Windows run gofer under a
// service manager (nssm / sc) instead of `-d`.
var errNotSupported = errors.New("daemon mode (-d) not supported on windows; run as a service")

func reexecDetached(string) (*exec.Cmd, error) { return nil, errNotSupported }

// PIDAlive cannot be determined cheaply without a service manager; report false
// so `stop` treats a stale pidfile as not-running rather than blocking.
func PIDAlive(int) bool { return false }

// Terminate is unsupported on Windows (see errNotSupported).
func Terminate(int) error { return errNotSupported }
