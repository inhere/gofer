//go:build windows

package hookrelay

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procGetLastInputInfo = user32.NewProc("GetLastInputInfo")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procGetTickCount64   = kernel32.NewProc("GetTickCount64")
)

// lastInputInfo mirrors Win32 LASTINPUTINFO.
type lastInputInfo struct {
	cbSize uint32
	// dwTime is the tick (ms) of the last input event; it is 32-BIT and wraps
	// every ~49.7 days, which is why the diff is taken against the 64-bit tick.
	dwTime uint32
}

// probeSystemIdle reads the tick of the last keyboard/mouse input and subtracts
// it from the current uptime. The subtraction happens in the tick's own 32-bit
// space — LASTINPUTINFO.dwTime is the low 32 bits of the same counter — so the
// ~49.7-day wrap of that field can never produce a bogus huge idle: a machine
// idle for longer than the wrap reports a small idle, which conservatively means
// "not armed". Instant: no process, no window station requirements.
func probeSystemIdle() int64 {
	var li lastInputInfo
	li.cbSize = uint32(unsafe.Sizeof(li))
	ok, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li)))
	if ok == 0 {
		return -1
	}
	now, _, _ := procGetTickCount64.Call()
	if now == 0 {
		return -1
	}
	return int64(uint32(now)-li.dwTime) / 1000
}
