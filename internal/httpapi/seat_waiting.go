package httpapi

import (
	"github.com/inhere/gofer/internal/inbox"
	"github.com/inhere/gofer/internal/today"
)

// waitingSubjectsFor is the 「等我」 judgement (inbox.WaitingSubjects) outside a request: the
// subjects waiting for caller, inside the caller's project ceiling ("" = no ceiling, the
// whole server). It is what the seat status and the notice scan read, so they agree with the
// lists a request builds (waitingFor).
func (s *Server) waitingSubjectsFor(caller string) ([]inbox.Subject, error) {
	if s.jobs == nil || s.jobs.Meta() == nil || s.today == nil {
		return nil, nil
	}
	in := inbox.WaitingInput{Lang: s.textLanguage(""), Store: s.jobs.Meta()}
	if svc := s.stages(); svc != nil {
		in.Stage = svc
	}
	q := today.Query{Lang: in.Lang}
	if cfg := s.serverCfg(); cfg != nil && caller != "" {
		if ceil, ok := cfg.CallerCeiling(caller); ok && ceil != nil && len(ceil.Projects) > 0 {
			in.Allow = func(project string) bool { return ceilingAllowsProject(ceil, project) }
			q.ProjectAllowed = in.Allow
			if s.projects != nil {
				q.Projects = s.scopedProjectKeys(in.Allow)
			}
		}
	}
	resp, err := s.today.Today(q)
	if err != nil {
		return nil, err
	}
	subjects, _, err := inbox.WaitingSubjects(in, resp.Cards)
	return subjects, err
}

// seatWaiting adapts the waiting judgement to seat.WaitingLookup: the category of every ref
// (terminal session id, ACP job id) that waits for caller.
type seatWaiting struct{ s *Server }

func (w seatWaiting) WaitingSubjects(caller string, refs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(refs) == 0 {
		return out, nil
	}
	subjects, err := w.s.waitingSubjectsFor(caller)
	if err != nil {
		return nil, err
	}
	want := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		want[r] = struct{}{}
	}
	for _, sub := range subjects {
		if sub.Type != inbox.SubjectSession && sub.Type != inbox.SubjectJob {
			continue
		}
		if _, ok := want[sub.Ref]; ok {
			out[sub.Ref] = string(sub.Category)
		}
	}
	return out, nil
}
