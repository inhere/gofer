package trigger

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// ErrOwnerDenied is the permanent refusal of a trigger whose owner caller may not submit
// jobs right now (undeclared, root, or without job.submit). HTTP maps it to 403.
var ErrOwnerDenied = errors.New("trigger owner is not permitted to submit jobs")

// JobGate is what admission needs from the job layer.
type JobGate interface {
	PreflightFixedRequest(job.JobRequest) error
	Submit(job.JobRequest) (job.JobResult, error)
}

// PermissionFunc answers whether a user-class caller currently holds a permission,
// with the live effective-permission model (config-derived plus database grants). It is
// injected by the process assembly, so this package depends on neither httpapi nor the
// grant store.
type PermissionFunc func(callerID, permission string) bool

// Admission is the owner-admission seam of design 3.2: the trigger's fixed request is
// judged against the OWNER's current effective permission and ceiling before the job
// layer takes it. The decision is made on every call from the live configuration and
// grants, never from a snapshot taken at load, so revoking job.submit (or tightening the
// ceiling) stops the next invocation.
type Admission struct {
	cfg  func() *config.Config
	jobs JobGate
	can  PermissionFunc
}

// NewAdmission builds the admission over the live config, the job layer and the
// permission model.
func NewAdmission(cfg func() *config.Config, jobs JobGate, can PermissionFunc) *Admission {
	return &Admission{cfg: cfg, jobs: jobs, can: can}
}

// CheckOwner refuses an owner that is not a declared, non-root caller holding job.submit.
func (a *Admission) CheckOwner(owner string) error {
	owner = strings.TrimSpace(owner)
	var cfg *config.Config
	if a != nil && a.cfg != nil {
		cfg = a.cfg()
	}
	if cfg == nil || a.can == nil || owner == "" {
		return fmt.Errorf("%w: no owner", ErrOwnerDenied)
	}
	declared := false
	for _, c := range cfg.Server.Callers {
		if c.ID == owner {
			declared = true
			break
		}
	}
	if !declared {
		return fmt.Errorf("%w: caller %q is not declared", ErrOwnerDenied, owner)
	}
	if slices.Contains(cfg.Server.RootAdminCallers(), owner) {
		return fmt.Errorf("%w: caller %q is a root administrator", ErrOwnerDenied, owner)
	}
	if !a.can(owner, authz.PermJobSubmit) {
		return fmt.Errorf("%w: caller %q lacks %s", ErrOwnerDenied, owner, authz.PermJobSubmit)
	}
	return nil
}

// Preflight judges the fixed request without submitting: owner permission, then the job
// layer's side-effect-free ceiling / template / project / agent / runner check.
func (a *Admission) Preflight(_ context.Context, req job.JobRequest) error {
	if err := a.CheckOwner(req.OriginCaller); err != nil {
		return err
	}
	return a.jobs.PreflightFixedRequest(req)
}

// Submit re-checks the owner (it may have been revoked since Preflight) and submits. The
// job layer repeats the ceiling admission against req.OriginCaller.
func (a *Admission) Submit(_ context.Context, req job.JobRequest) (string, error) {
	if err := a.CheckOwner(req.OriginCaller); err != nil {
		return "", err
	}
	res, err := a.jobs.Submit(req)
	if err != nil {
		return "", err
	}
	return res.ID, nil
}
