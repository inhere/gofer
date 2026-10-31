package job

import (
	"github.com/inhere/gofer/internal/testutil/machinelang"
	"os"
	"testing"

	"github.com/inhere/gofer/internal/testutil/gitenv"
)

// TestMain: these tests commit in TempDir repositories; git's detached auto
// maintenance must not outlive them (see gitenv).
func TestMain(m *testing.M) {
	gitenv.DisableAutoMaintenance()
	machinelang.PinZh()
	os.Exit(m.Run())
}
