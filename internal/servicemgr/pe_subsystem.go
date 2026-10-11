package servicemgr

import "encoding/binary"

const (
	peSubsystemWindowsGUI = 2
	peSubsystemWindowsCUI = 3
)

// markWindowsGUI flips a console-subsystem PE image to the GUI subsystem in
// place and reports whether it did. Task Scheduler gives a console image a
// visible console window before any of its code runs, so the supervisor copy
// flashed a cmd window on every start and upgrade. A GUI image gets no console;
// the server it starts is unaffected (procattr already uses CREATE_NO_WINDOW).
// Anything that is not a console PE image is left untouched.
func markWindowsGUI(image []byte) bool {
	if len(image) < 0x40 || image[0] != 'M' || image[1] != 'Z' {
		return false
	}
	peOff := int(binary.LittleEndian.Uint32(image[0x3c:]))
	// Signature (4) + COFF file header (20); Subsystem sits at offset 68 of the
	// optional header in both PE32 and PE32+.
	opt := peOff + 24
	if peOff < 0 || opt+70 > len(image) || string(image[peOff:peOff+4]) != "PE\x00\x00" {
		return false
	}
	if magic := binary.LittleEndian.Uint16(image[opt:]); magic != 0x10b && magic != 0x20b {
		return false
	}
	if binary.LittleEndian.Uint16(image[opt+68:]) != peSubsystemWindowsCUI {
		return false
	}
	binary.LittleEndian.PutUint16(image[opt+68:], peSubsystemWindowsGUI)
	return true
}
