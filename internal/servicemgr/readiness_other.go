//go:build !windows && !linux

package servicemgr

import "errors"

func OwnsListeningPort(int, string, uint16) (bool, error) {
	return false, errors.New("native service port ownership is supported on Windows and Linux")
}
