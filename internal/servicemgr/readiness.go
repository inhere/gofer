//go:build windows || linux

package servicemgr

// OwnsListeningPort binds a health probe to the already verified server PID.
// Both native backends implement the platform-specific socket ownership check.
func OwnsListeningPort(pid int, host string, port uint16) (bool, error) {
	return ownsListeningPort(pid, host, port)
}
