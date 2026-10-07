package job

import (
	"fmt"

	"github.com/inhere/gofer/internal/daemon"
)

// ValidateUpgradeSource binds a direct local exec job to the CLI process that
// requested an upgrade. The control file's SourceJobID is never trusted alone.
// A wrapper or descendant cannot be excluded from drain with this seam.
func (s *Service) ValidateUpgradeSource(jobID string, initiatorPID int) error {
	if jobID == "" || initiatorPID <= 0 {
		return fmt.Errorf("upgrade source job and initiator pid are required")
	}
	s.mu.Lock()
	entry := s.jobs[jobID]
	s.mu.Unlock()
	if entry == nil {
		return fmt.Errorf("upgrade source job %q is not active in this server", jobID)
	}
	entry.mu.Lock()
	result, expected := entry.result, entry.process
	entry.mu.Unlock()
	if result.Status != StatusRunning || result.Runner != builtinLocalRunner || result.Agent != "exec" || expected.PID != initiatorPID {
		return fmt.Errorf("upgrade source is not the active direct local exec process")
	}
	record, ok, err := s.meta.GetJob(jobID)
	if err != nil {
		return err
	}
	if !ok || record.Status != StatusRunning || record.Runner != builtinLocalRunner || record.Agent != "exec" {
		return fmt.Errorf("upgrade source is not a durable running local exec job")
	}
	actual, err := daemon.InspectProcess(initiatorPID)
	if err != nil {
		return err
	}
	if expected != actual {
		return fmt.Errorf("upgrade source process identity changed")
	}
	return nil
}
