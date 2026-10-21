package job

import (
	"fmt"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/wsproto"
)

// WorkflowStepJobTag tags the internal exec job that runs a built-in workflow step
// (`uses: gofer/<name>`, design 2026-10-21 §一·5). It is gofer's own tag, like the
// delivery job's, so the step job is recognisable in the job list.
const WorkflowStepJobTag = "workflow-step"

// checkWorkflowStepWorker refuses a built-in workflow step headed for a worker whose
// protocol predates `gofer tool step` (G032: the worker binary has no such sub-command, so
// the step would fail mid-workflow instead of the submit being refused while nothing has
// started yet). An offline / unknown-version worker passes: its own dispatch-time check is
// the backstop.
func (s *Service) checkWorkflowStepWorker(cfg *config.Config, req JobRequest) error {
	if req.InternalExec != InternalExecWorkflowStep || !isWorkerRunner(cfg, req.Runner) || s.workers == nil {
		return nil
	}
	workerID := req.WorkerID
	if workerID == "" {
		workerID = cfg.Runners[req.Runner].WorkerID
	}
	if workerID == "" {
		return nil
	}
	caps, online := s.workers.Candidate(workerID)
	if !online || !caps.ProtocolKnown {
		return nil
	}
	if !wsproto.SupportsWorkflowStep(caps.ProtocolVersion) {
		return fmt.Errorf("%w: worker %s speaks protocol v%d, a built-in workflow step (uses:) needs v%d; upgrade the worker",
			ErrInvalidRequest, workerID, caps.ProtocolVersion, wsproto.WorkflowStepMinProtocolVersion)
	}
	return nil
}
