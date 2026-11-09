package workbench

import (
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// supervisionFunc answers what supervises a relay session. nil means "nothing does".
type supervisionFunc func(jobstore.AgentSession) sessionrelay.Supervision

// relaySupervision returns the supervision lookup of the relay behind the service, or
// nil when that relay cannot tell (a test double, or no relay wired): such a session is
// treated as not supervised.
func (s *Service) relaySupervision() supervisionFunc {
	if s == nil || s.relay == nil {
		return nil
	}
	sup, ok := s.relay.(interface {
		Supervision(jobstore.AgentSession) sessionrelay.Supervision
	})
	if !ok {
		return nil
	}
	return sup.Supervision
}

// relayTurnsNeedReply reports whether any open turn is marked as needing a reply.
func relayTurnsNeedReply(decisions []jobstore.PlanDecision) bool {
	for _, d := range decisions {
		if d.NeedsReply {
			return true
		}
	}
	return false
}

// supervisingOf converts the supervision fact to the thread field (nil when none).
func supervisingOf(v sessionrelay.Supervision) *Supervising {
	if !v.Any() {
		return nil
	}
	return &Supervising{Jobs: v.Jobs, Subagents: v.Subagents, Watches: v.Watches}
}
