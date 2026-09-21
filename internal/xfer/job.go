package xfer

import (
	"errors"

	"github.com/inhere/gofer/internal/jobstore"
)

// job.go is XFER-01 X2's job-side entry into the transfer journal: the pull a hub
// starts for a file the EXECUTING machine collected (`job run --collect`).
//
// It is the same staged get a client-side `gofer tool cp` pull uses — the worker
// reads the file out of ITS project root and uploads the bytes to the content
// endpoint, no new mechanism — with the job id recorded so the journal answers
// "which job did this transfer serve". The hub settles it synchronously (Deliver),
// copies the payload into the job's artifact directory and Releases the staged copy.

// StageGetForJob records a pull that belongs to a job. caller and job id are the
// same value: the audit trail then names the job, which is what the collect step
// acted for (there is no human behind it to name).
func (m *Manager) StageGetForJob(jobID, runner, projectKey, path string) (jobstore.XferRecord, error) {
	if jobID == "" {
		return jobstore.XferRecord{}, errors.New("xfer: StageGetForJob: empty job id")
	}
	return m.stageGet(jobID, jobID, runner, projectKey, path)
}
