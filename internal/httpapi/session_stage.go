package httpapi

import (
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/stagenote"
)

// stageBriefOf is the `stage` object of one session's view (see sessionView.Stage). It reads
// stored rows only — never a gate, never git — because the hooks read the view on every
// heartbeat. Nil when this server has no stage service or the session has nothing to show.
func (s *Server) stageBriefOf(a jobstore.AgentSession) *stagenote.Brief {
	svc := s.stages()
	if svc == nil {
		return nil
	}
	return svc.BriefForSession(a.SessionID, a.ProjectKey)
}
