package desktop

import (
	"testing"

	"github.com/inhere/gofer/internal/testutil/cjkscan"
)

// desktopCjkExempt lists the non-test files of this package that still hold Chinese
// string literals. The merged-popup words and the toast's open button live in
// internal/i18n (lang/*/notify.ini). Max is a ceiling AND a floor (see cjkscan.Check).
var desktopCjkExempt = map[string]cjkscan.Exempt{
	"toastps.go": {Max: 1, Why: "toastDisabledHint is an operator hint carried by a SkipError reason (logged once per cause, never shown in a popup), naming the Windows settings page in the Windows UI language of the pilot deployment; no language reaches the notifier's skip path yet"},
}

// TestNoNewChineseInDesktopPackage is the full-package scan guard of design §7.
func TestNoNewChineseInDesktopPackage(t *testing.T) {
	cjkscan.Check(t, ".", desktopCjkExempt)
}
