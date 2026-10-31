package httpapi

import (
	"testing"

	"github.com/inhere/gofer/internal/testutil/cjkscan"
)

// httpapiCjkExempt lists the non-test files of this package that still hold Chinese
// string literals. Error details, warnings and display labels of the errors / forms a
// person meets in the console (permission presets, messenger, workbench turns, tunnels,
// steward, worker upgrade, notification test, capture-box names …) live in internal/i18n
// (lang/*/err.ini, httpapi.ini). Max is a ceiling AND a floor (see cjkscan.Check).
var httpapiCjkExempt = map[string]cjkscan.Exempt{
	"authz_gen.go":                {Max: 51, Why: "the template of the generated docs/reference/permissions.md (go generate): a Chinese reference document, not a response; the document language is an authoring choice"},
	"agent_probe_handler.go":      {Max: 1, Why: "the probe prompt sent to the agent under test, a fixed protocol line whose reply (\"OK\") is matched; not shown to a person"},
	"runner_messenger_handler.go": {Max: 2, Why: "title / target of the session-listing job recorded in the job store (stored text, not a response); the job service has no per-request language"},
	"inbox_handler.go":            {Max: 1, Why: "home-page card title suffix (today cards): S3b scope (today / work / session page text)"},
	"today_handler.go":            {Max: 1, Why: "home-page lane label suffix: S3b scope (today / work / session page text)"},
	"session_handler.go":          {Max: 2, Why: "the session page's handed-off notice and its \"just now\": S3b scope (toSessionView / session page text)"},
	"session_inject.go":           {Max: 1, Why: "status detail of an injected message whose job record is gone: S3b scope (session page text)"},
	"work_dispatch_handler.go":    {Max: 2, Why: "work-item journal lines (dispatch results): S3b scope (work journal)"},
	"work_handler.go":             {Max: 1, Why: "work-item journal line (archived from the capture box): S3b scope (work journal)"},
	"work_wiring.go":              {Max: 10, Why: "the work summarizer host's refusal reasons, shown in the work settings: S3b scope (work)"},
}

// TestNoNewChineseInHttpapiPackage is the full-package scan guard of design §7: text a
// person reads belongs in internal/i18n, and a file not in httpapiCjkExempt must carry
// no Chinese string literal.
func TestNoNewChineseInHttpapiPackage(t *testing.T) {
	cjkscan.Check(t, ".", httpapiCjkExempt)
}
