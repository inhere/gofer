package servicemgr

import (
	"encoding/binary"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func fakePE(magic, subsystem uint16) []byte {
	image := make([]byte, 0x40+24+240)
	copy(image, "MZ")
	binary.LittleEndian.PutUint32(image[0x3c:], 0x40)
	copy(image[0x40:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(image[0x40+24:], magic)
	binary.LittleEndian.PutUint16(image[0x40+24+68:], subsystem)
	return image
}

func TestMarkWindowsGUI(t *testing.T) {
	for _, magic := range []uint16{0x10b, 0x20b} {
		image := fakePE(magic, peSubsystemWindowsCUI)
		assert.True(t, markWindowsGUI(image))
		assert.Eq(t, uint16(peSubsystemWindowsGUI), binary.LittleEndian.Uint16(image[0x40+24+68:]))
		// Already GUI: nothing left to change.
		assert.False(t, markWindowsGUI(image))
	}

	// Not a console PE image: copied verbatim.
	native := fakePE(0x20b, 1)
	assert.False(t, markWindowsGUI(native))
	assert.Eq(t, uint16(1), binary.LittleEndian.Uint16(native[0x40+24+68:]))
	assert.False(t, markWindowsGUI([]byte("old-binary")))
	assert.False(t, markWindowsGUI(fakePE(0x107, peSubsystemWindowsCUI)))
	truncated := fakePE(0x20b, peSubsystemWindowsCUI)[:0x40+24+40]
	assert.False(t, markWindowsGUI(truncated))
	badOffset := fakePE(0x20b, peSubsystemWindowsCUI)
	binary.LittleEndian.PutUint32(badOffset[0x3c:], 0xfffffff0)
	assert.False(t, markWindowsGUI(badOffset))
}
