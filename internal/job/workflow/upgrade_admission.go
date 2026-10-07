package workflow

import (
	job "github.com/inhere/gofer/internal/job"
)

type upgradeAdmissionOps interface {
	BeginUpgradeWork() (*job.AdmissionPermit, error)
	SubmitWithPermit(*job.AdmissionPermit, job.JobRequest) (job.JobResult, error)
}

type admittedJobOps struct {
	JobOps
	gate   upgradeAdmissionOps
	permit *job.AdmissionPermit
}

func (a admittedJobOps) Submit(req job.JobRequest) (job.JobResult, error) {
	return a.gate.SubmitWithPermit(a.permit, req)
}

// admitted returns a shallow Engine view whose step submissions share one
// permit with the preceding AdvanceStep or child workflow insertion. Nested
// workflows reuse the view, so the gate is never recursively acquired.
func (e *Engine) admitted() (*Engine, func(), error) {
	if e.admission != nil {
		if e.admission.Released() {
			return e.baseEngine().admitted()
		}
		return e, func() {}, nil
	}
	gate, ok := e.ops.(upgradeAdmissionOps)
	if !ok {
		return e, func() {}, nil
	} // non-Service test hosts have no upgrade gate
	permit, err := gate.BeginUpgradeWork()
	if err != nil {
		return nil, nil, err
	}
	view := *e
	view.admission = permit
	view.base = e.baseEngine()
	view.ops = admittedJobOps{JobOps: e.ops, gate: gate, permit: permit}
	return &view, permit.Release, nil
}
