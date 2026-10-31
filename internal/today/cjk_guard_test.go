package today

import (
	"testing"

	"github.com/inhere/gofer/internal/testutil/cjkscan"
)

// todayCjkExempt lists the non-test files that still hold Chinese string literals. The
// card tags, titles, summaries, action labels, block reasons, lanes and status alerts live
// in internal/i18n (today.ini). Max is a ceiling AND a floor (see cjkscan.Check).
var todayCjkExempt = map[string]cjkscan.Exempt{
	"memory_suggest.go": {Max: 1, Why: "ErrMemorySuggestionStale: an error sentinel whose text httpapi returns as the error detail and stores in the suggestion note; error detail text is a separate migration"},
}

// TestNoNewChineseInTodayPackage is the full-package scan guard (design §7).
func TestNoNewChineseInTodayPackage(t *testing.T) {
	cjkscan.Check(t, ".", todayCjkExempt)
}
