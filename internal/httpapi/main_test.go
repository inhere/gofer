package httpapi

import (
	"github.com/inhere/gofer/internal/testutil/machinelang"
	"os"
	"testing"

	"github.com/inhere/gofer/internal/testutil/gitenv"
)

// TestMain: these tests commit in TempDir repositories; git's detached auto
// maintenance must not outlive them (see gitenv). It also installs the decision recorder
// (the design's 调用断言): the hook is installed once, before any test runs, and is never
// replaced, so the recorded decisions are per request (authz_decisions_test.go).
func TestMain(m *testing.M) {
	gitenv.DisableAutoMaintenance()
	installDecisionRecorder()
	machinelang.PinZh()
	os.Exit(m.Run())
}
