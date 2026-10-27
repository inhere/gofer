package tracker

import (
	"os"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/testutil/gitenv"
)

// TestMain: these tests commit in TempDir repositories; git's detached auto
// maintenance must not outlive them (see gitenv).
//
// It also pins GOFER_LANG to zh-CN: the memory text `gofer memory` / `repo
// status` print follows GOFER_LANG → the machine's locale → the default, so an
// assertion on a Chinese message would otherwise pass on a zh_CN host and fail on
// an English-locale CI (G053). A test that exercises English or the language
// chain sets GOFER_LANG itself with t.Setenv.
func TestMain(m *testing.M) {
	gitenv.DisableAutoMaintenance()
	_ = os.Setenv(i18n.EnvLanguageVar, string(i18n.ZhCN))
	_ = os.Unsetenv("LANGUAGE")
	os.Exit(m.Run())
}
