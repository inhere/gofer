package ndjsonfilter

import "github.com/inhere/gofer/internal/runner"

// meterEvent feeds the job's budget meter (N2 §B) from one parsed stream line. It runs
// BEFORE the keep whitelist and the projector, on every parsed object, so the meter
// sees the accounting whatever the event stream chooses to keep — a budget must not
// depend on a log-volume setting.
//
// What counts as a model request and where the tokens are, per stream dialect:
//   - claude: an `assistant` line is one content block of a message; the message `id`
//     dedupes the several lines of one message, and `message.usage` is its (growing)
//     usage. The terminal `result` row carries the run's authoritative tally and the
//     only cost claude reports.
//   - omp: a completed assistant `message_end` is one request, its usage the running
//     tally (see the omp projector).
//   - anything else with a usage path: the path's object is the running tally; no turn
//     count (the stream has no notion of one we can rely on).
func (f *Filter) meterEvent(typ string, obj map[string]any) {
	if f.meter == nil {
		return
	}
	switch f.projKind {
	case ProjectorClaude:
		switch typ {
		case "assistant":
			msg, _ := obj["message"].(map[string]any)
			id, _ := msg["id"].(string)
			var u *runner.Usage
			if block, ok := msg["usage"].(map[string]any); ok {
				u = runner.UsageFromObject(block, runner.UsageSourceNDJSONClaude)
			}
			f.meter.AddMessage(id, u)
		case "result":
			f.meter.SetTally(claudeUsage(obj))
		}
	case ProjectorOMP:
		if typ == "message_end" && pathString(obj, "message", "role") == "assistant" {
			f.meter.AddTurn()
			f.meter.SetTally(usageAt(obj, runner.UsageSourceNDJSONOMP, "message", "usage"))
		}
	}
	if f.usageAt != nil && f.projKind != ProjectorClaude && f.projKind != ProjectorOMP {
		f.meter.SetTally(usageAt(obj, f.usageSrc, f.usageAt...))
	}
}
