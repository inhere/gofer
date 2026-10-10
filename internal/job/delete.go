package job

import (
	"errors"
	"fmt"
	"time"
)

// ErrJobFinishing is returned (wrapped) by DeleteJob when the job already reads as
// finished in memory but its finish has not completed within deleteFinishWait. The
// message keeps the jobstore delete prefix, so the HTTP layer answers it with 409
// like any other "not deletable yet" refusal.
var ErrJobFinishing = errors.New("job is still finishing")

// deleteFinishWait bounds how long DeleteJob waits for a finishing job (a var so a
// test can shorten it).
var deleteFinishWait = 2 * time.Second

// DeleteJob removes a terminal job's durable record (jobstore.DeleteJob keeps the
// job.deleted audit) and then drops its in-memory entry.
//
// finish flips the in-memory status to terminal BEFORE the row is durable, and keeps
// writing for the job after that (credential revoke, metrics, todo, knowledge, agent
// health, hooks). A delete landing in that window used to be refused as "not terminal"
// while GET already answered done, and a delete just after the row landed left those
// trailing writes recreating rows for a job that no longer existed. So when the entry
// reads as finished, DeleteJob first waits (bounded) for its execute goroutine to end;
// a finish that outlasts the bound is answered with ErrJobFinishing.
func (s *Service) DeleteJob(id, by string) error {
	if e := s.entry(id); e != nil && isFinished(e.snapshot().Status) {
		t := time.NewTimer(deleteFinishWait)
		select {
		case <-e.done:
			t.Stop()
		case <-t.C:
			return fmt.Errorf("jobstore: delete: job %q: %w", id, ErrJobFinishing)
		}
	}
	if err := s.meta.DeleteJob(id, by); err != nil {
		return err
	}
	if e := s.entry(id); e != nil && isFinished(e.snapshot().Status) {
		s.mu.Lock()
		if s.jobs[id] == e {
			delete(s.jobs, id)
		}
		s.mu.Unlock()
	}
	return nil
}
