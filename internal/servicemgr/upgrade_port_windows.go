//go:build windows

package servicemgr

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const tcpTableOwnerPIDListener = 3

var getExtendedTCPTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

// ownsListeningPort binds readiness to the OS TCP listener's owning PID, not
// simply a /health response from some other process on the configured port.
func ownsListeningPort(pid int, host string, port uint16) (bool, error) {
	var size uint32
	rc, _, _ := getExtendedTCPTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, syscall.AF_INET, tcpTableOwnerPIDListener, 0)
	if rc != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) || size < 4 {
		return false, fmt.Errorf("TCP table sizing failed: %d", rc)
	}
	buf := make([]byte, size)
	rc, _, _ = getExtendedTCPTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0,
		syscall.AF_INET, tcpTableOwnerPIDListener, 0)
	if rc != 0 {
		return false, fmt.Errorf("read TCP owner table: %d", rc)
	}
	count := binary.LittleEndian.Uint32(buf[:4])
	if uint64(count)*24+4 > uint64(len(buf)) {
		return false, errors.New("truncated TCP owner table")
	}
	want := net.ParseIP(host)
	if want == nil {
		return false, fmt.Errorf("non-IP managed listen host %q", host)
	}
	for i := uint32(0); i < count; i++ {
		row := buf[4+int(i)*24:][:24]
		localPort := binary.BigEndian.Uint16(row[8:10])
		owner := binary.LittleEndian.Uint32(row[20:24])
		if localPort != port || owner != uint32(pid) {
			continue
		}
		local := net.IPv4(row[4], row[5], row[6], row[7])
		if local.Equal(want) || (local.IsUnspecified() && !want.IsUnspecified()) {
			return true, nil
		}
	}
	return false, nil
}
