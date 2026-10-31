package job

import (
	"testing"

	"github.com/inhere/gofer/internal/testutil/cjkscan"
)

// jobCjkExempt lists the non-test files of this package that still hold Chinese
// string literals. The notification titles / bodies / link labels (delivery.go,
// events.go, notify_dispatch.go) live in lang/*/notify.ini since i18n stage 3 (S3c). The agent-facing prompts (leader, plan rules, skills / tools
// sections, comment dispatch, wrap-up) live in internal/i18n; what is left is either
// server-side display text waiting for i18n stage 3 or a literal the code matches
// byte-exactly. Max is a ceiling AND a floor (see cjkscan.Check): migrating text out
// lowers it, new Chinese turns the test red.
var jobCjkExempt = map[string]cjkscan.Exempt{
	"config.go":           {Max: 1, Why: "operator-facing config error (worker policy pending); stage 3 server display text"},
	"delivery_check.go":   {Max: 5, Why: "delivery-check progress lines recorded in the job's events for people; stage 3 server display text"},
	"model.go":            {Max: 15, Why: "status labels shown to people (StatusLabel); stage 3 server display text"},
	"reviewhints.go":      {Max: 2, Why: "\"验证\" is a keyword matched against todo text (recognition, not output) and the truncation note of a review hint; stage 3"},
	"rules.go":            {Max: 1, Why: "rulesPromptHeader: fixed literal recognised structurally by the skills-manifest insertion and by older stored prompts; changing it breaks recognition"},
	"session_recovery.go": {Max: 1, Why: "error text returned to the operator after a worker restart; stage 3 server display text"},
	"submit.go":           {Max: 2, Why: "submit validation errors returned to the caller; stage 3 server display text"},
	"todo_continue.go":    {Max: 23, Why: "reasons why a todo could not continue its predecessor's session, stored as a note for people; stage 3 server display text"},
	"todolink.go":         {Max: 1, Why: "todo note appended when the linked job awaits review; stage 3 server display text"},
	"worktree.go":         {Max: 2, Why: "worktree check error and the merge note; stage 3 server display text"},
}

// TestNoNewChineseInJobPackage is the full-package scan guard of design §7 (the Go
// counterpart of web/src/i18n/noHardcodedCjk.test.ts): text for an agent belongs in
// internal/i18n, and a file not in jobCjkExempt must carry no Chinese string literal.
func TestNoNewChineseInJobPackage(t *testing.T) {
	cjkscan.Check(t, ".", jobCjkExempt)
}
