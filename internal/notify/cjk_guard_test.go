package notify

import (
	"testing"

	"github.com/inhere/gofer/internal/testutil/cjkscan"
)

// notifyCjkExempt lists the non-test files of this package that still hold Chinese
// string literals. The notification titles, bodies, link labels and the truncation
// marker live in internal/i18n (lang/*/notify.ini); what is left is the bilingual
// event catalog. Max is a ceiling AND a floor (see cjkscan.Check).
var notifyCjkExempt = map[string]cjkscan.Exempt{
	"catalog.go": {Max: 46, Why: "the event catalog is bilingual data: every entry carries its Zh and En text side by side (the console picks one by language), so the Chinese literals are one half of a translation table, not untranslated output"},
}

// TestNoNewChineseInNotifyPackage is the full-package scan guard of design §7: text a
// person reads belongs in internal/i18n, and a file not in notifyCjkExempt must carry
// no Chinese string literal.
func TestNoNewChineseInNotifyPackage(t *testing.T) {
	cjkscan.Check(t, ".", notifyCjkExempt)
}
