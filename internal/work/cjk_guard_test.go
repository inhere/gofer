package work

import (
	"testing"

	"github.com/inhere/gofer/internal/testutil/cjkscan"
)

// workCjkExempt lists the non-test files that still hold Chinese string literals. The
// journal / activity-log text, report notes, request lines, health reasons, the digest
// and the notifications live in internal/i18n (work.ini); what is left is either text
// an agent reads (the summarizer prompt) or text that is matched / stored as an
// identifier. Max is a ceiling AND a floor (see cjkscan.Check).
var workCjkExempt = map[string]cjkscan.Exempt{
	"noise.go":             {Max: 4, Why: "draft-exclusion reasons that only reach the server log (slog), never a person or the web"},
	"steward.go":           {Max: 2, Why: "labels inside the prompt material handed to the steward agent (agent-facing prompt, not display text)"},
	"summarize.go":         {Max: 22, Why: "the summarizer agent's prompt and material labels (agent-facing), plus summarize status / failure reasons returned to the caller and stored in the request ledger; the reasons need the ctx plumbed through StartSummarize first"},
	"summarize_session.go": {Max: 3, Why: "retry nudge sent to the summarizer agent and its session errors (agent-facing / internal errors)"},
	"x2.go":                {Max: 1, Why: "completionBy is the stored author identifier of the completion write-back suggestion; changing it would split old and new rows"},
}

// TestNoNewChineseInWorkPackage is the full-package scan guard (design §7).
func TestNoNewChineseInWorkPackage(t *testing.T) {
	cjkscan.Check(t, ".", workCjkExempt)
}
