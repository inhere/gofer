package hookrelay

import (
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/stagenote"
)

// stageTurnReminder appends the stage-flow line to the context of a UserPromptSubmit: the
// step the session's work item is on, and the commands that finish, roll back or block it.
// It goes LAST (after the job notices, mail and memories) so it is the freshest thing the
// agent reads, and it is added for harness-generated prompts too — the flow does not pause
// while a job notice is being delivered. Nothing is added for a session with no flow in
// progress, or for an agent whose hook output does not reach the model.
func (r *runner) stageTurnReminder(res Result, a client.AgentSession) Result {
	if a.Stage == nil || !a.Stage.InFlow() || !CatchUpAgent(r.p.dialect()) {
		return res
	}
	return r.appendContext(res, stagenote.Line(r.lang(), *a.Stage))
}

// stageStartReminder appends the SessionStart stage line: the current step of a flow in
// progress, else — in a project with the flow on — the suggestion to start one before
// implementation work. A session in a seat gets it inside its seat block instead
// (seat.Section), so it is not said twice.
func (r *runner) stageStartReminder(res Result, a client.AgentSession) Result {
	if a.Seat != nil || !CatchUpAgent(r.p.dialect()) {
		return res
	}
	return r.appendContext(res, stagenote.SessionStartLine(r.lang(), a.Stage))
}

// appendContext adds one block after whatever the context already holds.
func (r *runner) appendContext(res Result, text string) Result {
	if text == "" {
		return res
	}
	if res.Context != "" {
		res.Context += "\n\n"
	}
	res.Context += text
	return res
}
