package sessionrelay

import (
	"testing"

	"github.com/inhere/gofer/internal/testutil/cjkscan"
)

// relayCjkExempt lists the non-test files that still hold Chinese string literals: the
// delivery outcomes and failure reasons the relay reports to the console / the caller.
// They are server display text for i18n stage 3. Max is a ceiling AND a floor.
var relayCjkExempt = map[string]cjkscan.Exempt{
	"deliver.go":           {Max: 4, Why: "delivery failure reasons reported to the caller; stage 3 server display text"},
	"deliver_raw.go":       {Max: 2, Why: "relay-job outcome lines recorded as the delivery detail; stage 3 server display text"},
	"deliver_reconcile.go": {Max: 6, Why: "reconcile outcome lines recorded as the delivery detail; stage 3 server display text"},
	"message.go":           {Max: 9, Why: "message dispatch failure reasons and the sent note; stage 3 server display text"},
	"nudge.go":             {Max: 1, Why: "the pause reason of a nudge after consecutive failures; stage 3 server display text"},
	"origin_cwd.go":        {Max: 8, Why: "notes explaining how the original start directory was verified; stage 3 server display text"},
	"reply.go":             {Max: 1, Why: "truncation marker appended to a long reply; stage 3 server display text"},
}

// TestNoNewChineseInSessionrelayPackage is the full-package scan guard (design §7).
func TestNoNewChineseInSessionrelayPackage(t *testing.T) {
	cjkscan.Check(t, ".", relayCjkExempt)
}
