package mail

import (
	"testing"

	"github.com/inhere/gofer/internal/testutil/cjkscan"
)

// mailCjkExempt lists the non-test files that still hold Chinese string literals; the
// resident-session prompt, bounce reasons, hold and deliverer texts live in
// internal/i18n. Max is a ceiling AND a floor (see cjkscan.Check).
var mailCjkExempt = map[string]cjkscan.Exempt{
	"address.go": {Max: 2, Why: "an address parse error returned to the sender and the offline hint of a recipient label; stage 3 server display text"},
	"origin.go":  {Max: 1, Why: "the \"(caller ...)\" suffix of a sender label shown in the console; stage 3 server display text"},
	"service.go": {Max: 3, Why: "a send error returned to the caller, the withdrawn-letter log line and the excerpt label of a notice; stage 3 server display text"},
}

// TestNoNewChineseInMailPackage is the full-package scan guard (design §7).
func TestNoNewChineseInMailPackage(t *testing.T) {
	cjkscan.Check(t, ".", mailCjkExempt)
}
