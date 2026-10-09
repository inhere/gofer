package httpapi

import (
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/work"
)

// wireWorkJobOutcomes journals every finished job on the work items it belongs to
// (WORK-06): the job service's terminal hook hands the result over, the work service
// decides the line and its level. Assembly only — no rule lives here.
func (s *Server) wireWorkJobOutcomes(jobs *job.Service) {
	if s.work == nil || jobs == nil {
		return
	}
	w := s.work
	jobs.OnTerminal(func(r job.JobResult) { w.NoteJobOutcome(workJobOutcome(r)) })
}

// workJobOutcome adapts a job result to the work service's outcome shape.
func workJobOutcome(r job.JobResult) work.JobOutcome {
	return work.JobOutcome{
		ID: r.ID, Agent: r.Agent, Title: r.Title, Status: r.Status, PlanID: r.PlanID,
		FailureClass: r.FailureClass, Error: r.Error, Commits: max(len(r.Commits), r.CommitsAhead),
		Reviewed: r.ReviewedAt > 0,
	}
}
