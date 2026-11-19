package hookrelay

import (
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/needsreply"
)

// NeedsReplyMarker is the line an agent writes (alone, as the last line) when it
// cannot continue without a person's answer. Only such turns reach the "waiting
// for me" list; a plain report turn does not.
const NeedsReplyMarker = needsreply.Marker

// NeedsReplyReminder is the line SessionStart adds to additionalContext so the
// agent knows the marker. Generic wording: it names no project or tool.
var NeedsReplyReminder = needsreply.Reminder

// needsReplyReminder appends NeedsReplyReminder to the SessionStart context after
// whatever is already there, only for agents whose hook output reaches the model.
func (r *runner) needsReplyReminder(res Result, _ client.AgentSession) Result {
	if !CatchUpAgent(r.p.dialect()) {
		return res
	}
	if res.Context != "" {
		res.Context += "\n\n"
	}
	res.Context += NeedsReplyReminder
	return res
}
