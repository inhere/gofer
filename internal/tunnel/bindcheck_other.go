//go:build !windows

package tunnel

import (
	"errors"
	"syscall"
)

func isAddrNotAvail(err error) bool { return errors.Is(err, syscall.EADDRNOTAVAIL) }
