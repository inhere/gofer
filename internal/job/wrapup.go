package job

import (
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
)

// wrapup.go is gofer-gk74: the AGENT's own reason for ending a run early, and how it is
// shown. The signal is read out of the agent's structured stream by the ndjson capture
// (ndjsonfilter.Signals.WrapUp) and lands on JobResult.WrapUp.
//
// Why it deserves a field of its own: a job that hits the agent's own step / token /
// time ceiling still ends `done` with a report — the report is real work, but the task
// is probably unfinished, and nothing in the job said so. Before this, the only trace
// was a line in the middle of the agent's own output, so a reader had to know the
// dialect to spot it (and an agent that hid it looked like a normal success).

// WrapUpText renders a wrap-up reason as the human line `job show` / `job review` / the
// web detail print. An unknown reason is shown verbatim: a future agent ceiling must
// still be visible.
func WrapUpText(reason string) string { return WrapUpTextIn(i18n.Default, reason) }

// WrapUpTextIn is WrapUpText in lang.
func WrapUpTextIn(lang i18n.Lang, reason string) string {
	switch reason {
	case ndjsonfilter.WrapUpMaxSteps:
		return i18n.T(lang, "job.wrapup.max_steps")
	case ndjsonfilter.WrapUpMaxTokens:
		return i18n.T(lang, "job.wrapup.max_tokens")
	case ndjsonfilter.WrapUpBudget:
		return i18n.T(lang, "job.wrapup.budget")
	case ndjsonfilter.WrapUpTimeout:
		return i18n.T(lang, "job.wrapup.timeout")
	case "":
		return ""
	default:
		return i18n.T(lang, "job.wrapup.other", "reason", reason)
	}
}
