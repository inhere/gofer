package seat

import (
	"fmt"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// endedGrace is how long a seat's session may have been ended before the seat counts as free
// for a new session. A session that is merely offline never frees its seat.
const endedGrace = 7 * 24 * time.Hour

// Candidates lists the seats a terminal session could be bound to: seats of the session's own
// project (and caller) whose directory contains the session's directory, that are vacant or
// whose session ended more than 7 days ago. A seat whose session is only offline is not a
// candidate, nor is a seat of another agent kind.
func (s *Service) Candidates(sessionID string) ([]jobstore.Seat, error) {
	a, ok, err := s.store.GetAgentSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, sessionrelay.ErrUnknownSession
	}
	if a.ProjectKey == "" {
		return nil, nil
	}
	rel, ok := sessionrelay.RelToRoots(a.Cwd, s.roots(a.ProjectKey))
	if !ok {
		return nil, nil
	}
	seats, err := s.store.ListSeats(jobstore.SeatFilter{CallerID: a.CallerID, ProjectKey: a.ProjectKey})
	if err != nil {
		return nil, err
	}
	var matched []jobstore.Seat
	for _, x := range seats {
		if x.Kind != jobstore.SeatKindTerminal || !relUnder(rel, x.RelCwd) {
			continue
		}
		if x.Agent != "" && a.Agent != "" && x.Agent != a.Agent {
			continue
		}
		if x.CurrentRef == a.SessionID {
			continue
		}
		matched = append(matched, x)
	}
	open, err := s.openSeats(matched)
	if err != nil {
		return nil, err
	}
	var out []jobstore.Seat
	for i, x := range matched {
		if open[i] {
			out = append(out, x)
		}
	}
	return out, nil
}

// openSeats reports, for each seat, whether a new session may take it: nobody sits there, the
// sitting session is gone, or it ended more than 7 days ago.
func (s *Service) openSeats(seats []jobstore.Seat) ([]bool, error) {
	var sids, jids []string
	for _, x := range seats {
		switch {
		case x.CurrentRef == "":
		case x.Kind == jobstore.SeatKindACP:
			jids = append(jids, x.CurrentRef)
		default:
			sids = append(sids, x.CurrentRef)
		}
	}
	sessions, err := s.store.GetAgentSessionsByIDs(sids)
	if err != nil {
		return nil, err
	}
	jobs, err := s.store.GetJobsByIDs(jids)
	if err != nil {
		return nil, err
	}
	cutoff := s.d.Now().Add(-endedGrace).Unix()
	out := make([]bool, len(seats))
	for i, x := range seats {
		switch {
		case x.CurrentRef == "":
			out[i] = true
		case x.Kind == jobstore.SeatKindACP:
			rec, ok := jobs[x.CurrentRef]
			out[i] = !ok || (job.IsTerminal(rec.Status) && rec.EndedAt > 0 && rec.EndedAt < cutoff)
		default:
			a, ok := sessions[x.CurrentRef]
			out[i] = !ok || (a.State == jobstore.SessionEnded && a.EndedAt > 0 && a.EndedAt < cutoff)
		}
	}
	return out, nil
}

// BindCandidate binds a terminal session to the candidate seat, replacing a session that
// ended long ago. It refuses a seat that is not among the session's candidates.
func (s *Service) BindCandidate(seatID, sessionID, by string) (jobstore.Seat, error) {
	cands, err := s.Candidates(sessionID)
	if err != nil {
		return jobstore.Seat{}, err
	}
	for _, x := range cands {
		if x.ID == seatID {
			return s.Bind(seatID, sessionID, true, by)
		}
	}
	return jobstore.Seat{}, fmt.Errorf("%w: seat %s is not a candidate of session %s", jobstore.ErrSeatInvalid, seatID, strings.TrimSpace(sessionID))
}
