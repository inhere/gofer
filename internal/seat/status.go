package seat

import (
	"fmt"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/workbench"
)

// The state of the session sitting in a seat.
const (
	StateNone     = "none"
	StateRunning  = "running"
	StateAwaiting = "awaiting"
	StateOffline  = "offline"
	StateEnded    = "ended"
	// StateFailed is an ACP seat whose job ended on a failure (failed / timeout / any other
	// end that is about the work not holding up): the session is gone like StateEnded, but
	// the roster can show that it did not finish. Terminal sessions never read as failed.
	StateFailed = "failed"
)

// Status is a seat with what is derived from the session sitting in it.
type Status struct {
	Seat jobstore.Seat
	// State is the sitting session's state: none / running / awaiting / offline / ended / failed.
	State string
	// Waiting is the category WaitingLookup gave when the session is waiting for the viewer
	// ("" = not waiting).
	Waiting string
	// Unread is true when the session was active after the viewer last looked at its thread.
	Unread bool
	// ActivityAt is the sitting session's last activity (unix seconds, 0 = none).
	ActivityAt int64
	// ThreadID is the workbench thread of the sitting session ("" = vacant).
	ThreadID string
}

// Statuses computes the status of every seat for one viewer in a fixed number of queries:
// the sessions, the jobs, the viewer's thread prefs and the waiting lookup are each read once
// for the whole set.
func (s *Service) Statuses(viewer string, seats []jobstore.Seat) ([]Status, error) {
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
	var waiting map[string]string
	if s.d.Waiting != nil && viewer != "" && len(sids)+len(jids) > 0 {
		if waiting, err = s.d.Waiting.WaitingSubjects(viewer, append(append([]string(nil), sids...), jids...)); err != nil {
			return nil, fmt.Errorf("seat: waiting lookup: %w", err)
		}
	}
	seenAt := map[string]int64{}
	var baseline int64
	if viewer != "" && len(sids)+len(jids) > 0 {
		prefs, err := s.store.ListWorkbenchThreadPrefs(viewer)
		if err != nil {
			return nil, err
		}
		for _, p := range prefs {
			seenAt[p.ThreadID] = p.SeenAt
		}
		baseline, _, err = s.store.GetWorkbenchSeenBaseline(viewer)
		if err != nil {
			return nil, err
		}
	}
	out := make([]Status, 0, len(seats))
	for _, x := range seats {
		st := Status{Seat: x, State: StateNone}
		switch {
		case x.CurrentRef == "":
		case x.Kind == jobstore.SeatKindACP:
			if rec, ok := jobs[x.CurrentRef]; ok {
				st.State = acpState(rec)
				st.ActivityAt = max(rec.UpdatedAt, rec.EndedAt, rec.StartedAt)
				st.ThreadID = workbench.JobThreadID(rec.ID, rec.SessionID)
			}
		default:
			if a, ok := sessions[x.CurrentRef]; ok {
				st.State = terminalState(a.State)
				st.ActivityAt = a.ActivityAt()
				st.ThreadID = "r:" + a.SessionID
			}
		}
		st.Waiting = waiting[x.CurrentRef]
		if st.ThreadID != "" {
			seen := max(seenAt[st.ThreadID], baseline)
			st.Unread = st.ActivityAt > seen
		}
		out = append(out, st)
	}
	return out, nil
}

// Status is Statuses for one seat.
func (s *Service) Status(viewer string, x jobstore.Seat) (Status, error) {
	all, err := s.Statuses(viewer, []jobstore.Seat{x})
	if err != nil {
		return Status{}, err
	}
	return all[0], nil
}

func terminalState(state string) string {
	switch state {
	case jobstore.SessionWaitingReply, jobstore.SessionNeedsAttention:
		return StateAwaiting
	case jobstore.SessionOffline:
		return StateOffline
	case jobstore.SessionEnded:
		return StateEnded
	default:
		return StateRunning
	}
}

// acpState maps the job sitting in an ACP seat to the seat's state. A finished job reads as
// failed when its end was about the work not holding up — failed / timeout, a budget or agent
// stop that never answered, a human's rejection — which job.TerminationReasonOf decides; done,
// cancelled and a session's own end (idle / max session time) stay ended.
func acpState(rec jobstore.JobRecord) string {
	switch {
	case !job.IsTerminal(rec.Status):
		if rec.Status == job.StatusAwaitingInput {
			return StateAwaiting
		}
		return StateRunning
	}
	switch job.TerminationReasonOf(rec) {
	case job.TerminationOK, job.TerminationCancelled, job.TerminationSessionEnded:
		return StateEnded
	default:
		return StateFailed
	}
}
