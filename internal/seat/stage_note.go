package seat

import (
	"context"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// The stage flow's "continue" turn (plan 2026-11-26-flow-progression-plan.md, F4) for a
// session that sits on a seat. It takes the delivery path of a due seat schedule
// (deliverNow) and, when the seat cannot take it, the same queue (jobstore.SeatPending,
// schedule_id "stage:<work item>", swept by FlushPending and sent on bind / when the session
// is back). A session on no seat is not this file's: the caller delivers to it directly.

// Outcomes of DeliverStageNote.
const (
	StageNoteDelivered = "delivered"
	StageNoteQueued    = "queued"
	// StageNoteGone: an ACP seat whose job has ended. A reminder is not worth restarting a
	// session for, so nothing is sent and nothing is queued.
	StageNoteGone = "gone"
)

// DeliverStageNote hands text (already carrying its system prefix) to the session ref when it
// sits on a seat: handled=false when it sits on none. An offline terminal session's text is
// queued, replacing an earlier queued reminder of the same work item.
func (s *Service) DeliverStageNote(ctx context.Context, workItemID, ref, step, text string) (outcome string, handled bool, err error) {
	x, ok, err := s.SeatOfRef(ref)
	if err != nil || !ok {
		return "", false, err
	}
	st, err := s.Status("", x)
	if err != nil {
		return "", true, err
	}
	if x.Kind == jobstore.SeatKindACP && (st.State == StateEnded || st.State == StateFailed) {
		return StageNoteGone, true, nil
	}
	id := jobstore.StagePendingID(workItemID)
	req := job.SeatDeliverRequest{SeatID: x.ID, Text: text, ScheduleID: id, ScheduleName: step}
	res, err := s.deliverNow(ctx, x, st, text, req)
	if err != nil {
		return "", true, err
	}
	if res.Outcome == jobstore.ScheduleResultOffline {
		// The row keeps the latest reminder only: a second one replaces the first's text.
		if err := s.store.DropSeatPendingOf(x.ID, id); err != nil {
			return "", true, err
		}
		if _, err := s.store.EnqueueSeatPending(x.ID, id, step, text, ""); err != nil {
			return "", true, err
		}
		return StageNoteQueued, true, nil
	}
	if err := s.store.DropSeatPendingOf(x.ID, id); err != nil {
		return "", true, err
	}
	return StageNoteDelivered, true, nil
}

// stageNoteDue reports whether a queued stage reminder is still worth sending: the flow is
// still on the step it was queued for and the seat's session can be spoken to without
// restarting anything.
func (s *Service) stageNoteDue(p jobstore.SeatPending, x jobstore.Seat, st Status) bool {
	wid, ok := jobstore.StagePendingWorkItem(p.ScheduleID)
	if !ok {
		return true
	}
	if x.Kind == jobstore.SeatKindACP && (st.State == StateEnded || st.State == StateFailed) {
		return false
	}
	ws, found, err := s.store.GetWorkStage(wid)
	if err != nil {
		return true
	}
	return found && ws.Status == jobstore.StageActive && ws.StepName() == p.ScheduleName
}
