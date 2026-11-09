package today

import "github.com/inhere/gofer/internal/jobstore"

// relayGraceSec is how long a relay turn must have been open before it counts as
// "waiting for me": a turn that lasts less than this is usually answered at the
// keyboard before the person ever looks at the list.
const relayGraceSec = 300

// gateRelayTurns keeps only the open turns that belong in the waiting list: the agent
// marked the turn as needing a reply, nobody supervises the session, and the turn has
// been open for the grace period. It is evaluated on every build, so a session that
// gains a supervisor drops out at once and returns when the supervision ends.
//
// A turn held back ONLY by the grace period sets recheck_at (the earliest moment one of
// them qualifies), so a client can refetch exactly then. Turns held back by supervision
// or a missing marker do not: those change through session / job events.
func (b *builder) gateRelayTurns(sess *jobstore.AgentSession, turns []*jobstore.PlanDecision) []*jobstore.PlanDecision {
	var kept []*jobstore.PlanDecision
	supervisedKnown, supervised := false, false
	for _, d := range turns {
		if !d.NeedsReply {
			continue
		}
		if !supervisedKnown { // one lookup per session, and only if a marked turn exists
			supervisedKnown = true
			if sess != nil && b.s.d.Supervisor != nil {
				supervised = b.s.d.Supervisor.Supervision(*sess).Any()
			}
		}
		if supervised {
			continue
		}
		if at := d.AskedAt + relayGraceSec; b.now < at {
			if b.recheckAt == 0 || at < b.recheckAt {
				b.recheckAt = at
			}
			continue
		}
		kept = append(kept, d)
	}
	return kept
}
