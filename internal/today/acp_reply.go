package today

import (
	"net/url"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/waitgate"
	"github.com/inhere/gofer/internal/workbench"
)

// KindACPReply is a managed ACP / suga session job waiting on a person's reply: the job sits
// in awaiting_input, the turn that ended carried the needs-reply marker (recorded on the
// job.awaiting_input event, see jobstore.ListMarkedAwaitingTurns), and the turn has been open
// for the grace period. The card is built from stored rows only, so it is the same after a
// server restart. It enters the 「等我」 list under the answer class.
const KindACPReply = "acp_reply"

// acpReplies adds one card per managed session job that waits on a reply. A turn still inside
// the grace period adds none and sets recheck_at to the moment it qualifies.
func (b *builder) acpReplies() error {
	turns, err := b.store().ListMarkedAwaitingTurns()
	if err != nil {
		return err
	}
	for id, turn := range turns {
		if ripe, at := waitgate.Ripe(b.now, turn.At); !ripe {
			b.holdUntil(at)
			continue
		}
		rec, err := b.job(id)
		if err != nil {
			return err
		}
		if rec == nil {
			continue
		}
		thread := workbench.JobThreadID(rec.ID, rec.SessionID)
		c := Card{
			Key: KindACPReply + ":" + rec.ID, Kind: KindACPReply, Tag: b.tr("tag.acp_reply"), Title: jobTitle(*rec),
			ProjectKey: rec.ProjectKey, Agent: rec.Agent, WaitingSince: turn.At, ActivityAt: turn.At,
			Refs: Refs{JobID: rec.ID, ThreadID: thread},
			Actions: []Action{
				{ID: "open", Label: i18n.T(b.lang, "today.action.details"), Style: "link", Value: "/workbench?thread=" + url.QueryEscape(thread)},
			},
		}
		blk := b.blocks()
		blk.agent(c.Agent, b.now-turn.At)
		c.Blocks = blk.result()
		b.cards = append(b.cards, c)
	}
	return nil
}
