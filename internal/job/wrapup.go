package job

import "github.com/inhere/gofer/internal/runner/ndjsonfilter"

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
func WrapUpText(reason string) string {
	switch reason {
	case ndjsonfilter.WrapUpMaxSteps:
		return "步数上限（agent 自己收尾，任务可能未完成）"
	case ndjsonfilter.WrapUpMaxTokens:
		return "用量上限（agent 自己收尾，任务可能未完成）"
	case ndjsonfilter.WrapUpTimeout:
		return "时限（agent 自己收尾，任务可能未完成）"
	case "":
		return ""
	default:
		return reason + "（agent 自己收尾，任务可能未完成）"
	}
}
