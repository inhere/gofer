//go:build windows

package servicemgr

import (
	"debug/pe"
	"os"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func peSubsystem(t *testing.T, path string) uint16 {
	t.Helper()
	f, err := pe.Open(path)
	assert.Require(t, assert.NoErr(t, err))
	defer f.Close()
	switch h := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		return h.Subsystem
	case *pe.OptionalHeader32:
		return h.Subsystem
	}
	t.Fatalf("%s has no optional header", path)
	return 0
}

func TestCopySupervisorBinaryUsesGUISubsystem(t *testing.T) {
	// The test binary is a console image, like gofer.exe.
	source, err := os.Executable()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, uint16(pe.IMAGE_SUBSYSTEM_WINDOWS_CUI), peSubsystem(t, source))

	target := filepath.Join(t.TempDir(), "svc", "gofer-supervisor.exe")
	assert.Require(t, assert.NoErr(t, copySupervisorBinary(source, target)))
	assert.Eq(t, uint16(pe.IMAGE_SUBSYSTEM_WINDOWS_GUI), peSubsystem(t, target))
	assert.Eq(t, uint16(pe.IMAGE_SUBSYSTEM_WINDOWS_CUI), peSubsystem(t, source))

	// An unchanged source keeps the existing copy (it may be the running image).
	before, err := os.Stat(target)
	assert.Require(t, assert.NoErr(t, err))
	assert.Require(t, assert.NoErr(t, copySupervisorBinary(source, target)))
	after, err := os.Stat(target)
	assert.Require(t, assert.NoErr(t, err))
	assert.True(t, os.SameFile(before, after))
}
