package workbench

import (
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/waitgate"
)

// The workbench's attention is the same judgement as the 「等我」 list (today / inbox): a
// turn needs a person only when it is marked, has waited out the grace period
// (waitgate.Ripe) and nothing supervises the session. These helpers apply the grace rule to
// the two turn kinds the workbench projects; the facts come from the snapshot rows.

// relayTurnsRipe reports whether any open turn that needs a reply has waited out the grace
// period at now.
func relayTurnsRipe(decisions []jobstore.PlanDecision, now int64) bool {
	for _, d := range decisions {
		if !d.NeedsReply {
			continue
		}
		if ripe, _ := waitgate.Ripe(now, d.AskedAt); ripe {
			return true
		}
	}
	return false
}

// acpReplyAttention returns the attention item of a managed session job that waits on a
// reply: awaiting_input, its turn carries the marker (snapshot.AwaitingTurns) and the grace
// period is over. nil when it does not.
func acpReplyAttention(thread Thread, latest jobstore.JobRecord, turns map[string]jobstore.AwaitingTurn, now int64) *AttentionItem {
	if latest.Status != job.StatusAwaitingInput {
		return nil
	}
	turn, ok := turns[latest.ID]
	if !ok {
		return nil
	}
	if ripe, _ := waitgate.Ripe(now, turn.At); !ripe {
		return nil
	}
	return &AttentionItem{
		ThreadID: thread.ID, ProjectKey: thread.ProjectKey, Title: thread.Title, Status: StatusAwaitingInput,
		Action: ActionReply, WaitingSince: turn.At, JobID: latest.ID,
	}
}

// markAttention sets Thread.Attention on every projection that carries an attention item, so
// a client groups a thread under 「需要你」 by the server's verdict and not by its raw status.
func markAttention(ps []projection) []projection {
	for i := range ps {
		ps[i].thread.Attention = ps[i].attention != nil
	}
	return ps
}
