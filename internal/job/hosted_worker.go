package job

import (
	"fmt"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/wsproto"
)

// checkHostedWorker refuses a hosted session for a worker whose protocol cannot carry it:
// the worker would drop the field, add its own idle default and end the session after 30
// minutes. Like checkPermissionTierWorker it is the admission-time courtesy; the
// dispatch-time check in the worker runner is the backstop for a worker whose version is
// not known yet.
func (s *Service) checkHostedWorker(cfg *config.Config, req JobRequest) error {
	if !req.Hosted || !isWorkerRunner(cfg, req.Runner) || s.workers == nil {
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
	if !wsproto.SupportsHosted(caps.ProtocolVersion) {
		return fmt.Errorf("%w: worker %s speaks protocol v%d, a hosted session needs v%d; upgrade the worker or drop hosted",
			ErrInvalidRequest, workerID, caps.ProtocolVersion, wsproto.HostedMinProtocolVersion)
	}
	return nil
}
