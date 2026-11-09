package sessionrelay

import (
	"log/slog"

	"github.com/inhere/gofer/internal/jobstore"
)

// Supervision is the fact "someone is already watching this session's work":
// jobs in flight under its caller, running sub-agents, and job watches bound to
// the session. It is a fact, not a policy: it ignores SkipWhenSupervising and
// RelayMode, so the waiting list and the workbench can show it while the Stop
// gate keeps its own switches.
type Supervision struct {
	Jobs      int
	Subagents int
	Watches   int
}

// Any reports whether anything supervises the session.
func (v Supervision) Any() bool { return v.Jobs > 0 || v.Subagents > 0 || v.Watches > 0 }

// Supervision reads the supervision facts of a session now. A failed lookup
// counts as 0 for that item and is logged (GR109), the same stance as the Stop
// gate: a broken lookup must not invent supervision.
func (s *Service) Supervision(a jobstore.AgentSession) Supervision {
	v := Supervision{Subagents: s.SubagentCount(a.SessionID)}
	if a.CallerID != "" {
		since := int64(0)
		if s.SupervisingWindowSec > 0 {
			since = s.nowFn().Unix() - int64(s.SupervisingWindowSec)
		}
		n, err := s.store.CountActiveJobsByCaller(a.CallerID, since)
		if err != nil {
			slog.Warn("sessionrelay: count supervising jobs failed", "session_id", a.SessionID,
				"caller_id", a.CallerID, "err", err)
			n = 0
		}
		v.Jobs = n
	}
	watches, err := s.store.ListSessionJobWatches(a.SessionID)
	if err != nil {
		slog.Warn("sessionrelay: list session job watches failed", "session_id", a.SessionID, "err", err)
	}
	v.Watches = len(watches)
	return v
}
