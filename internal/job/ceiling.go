// Caller ceilings on the JOB side (design 2026-10-18, 上限所有者 / 准入).
//
// A ceiling is declared per caller in the configuration (server.callers[].ceiling,
// internal/config/ceiling.go). The job layer owns two halves of it: recording WHO a job
// belongs to (its ceiling owner, `origin_caller`) so a job gofer starts on a caller's
// behalf stays bounded, and ADMITTING a submission against the owners' ceilings at the
// single submit entry point every path funnels through.
package job

import (
	"errors"
	"fmt"
	"slices"

	"github.com/inhere/gofer/internal/config"
)

// ErrCeilingDenied is the permanent refusal of a submission that violates a caller
// ceiling: the owner may not reach the project, the runner, the worker or the agent, or
// may not run exec jobs. It is a SENTINEL (errors.Is) because every re-submission path
// must TERMINATE on it instead of retrying — the ceiling will not change by waiting (a
// retry sweep cancels the row, a plan item records a dispatch error, a schedule run is
// recorded as failed). HTTP maps it to 403.
var ErrCeilingDenied = errors.New("caller ceiling denied")

// System jobs are the jobs gofer starts for itself and for nobody else: they have no
// calling user, so they carry no ceiling owner and are admitted without a ceiling check.
// The list is a REGISTRY, not a comment: admission refuses an unknown kind, and refuses a
// registered one that carries an owner (a restricted caller's chain can therefore never
// dress itself up as a system job).
const (
	// SystemJobUpgrade is the server's own upgrade work (`serve upgrade`), started by the
	// operator on the server host, never by a caller's request.
	SystemJobUpgrade = "upgrade"
	// SystemJobCleanup is gofer's own retention / prune work.
	SystemJobCleanup = "cleanup"
)

// systemJobKinds is the exemption list. It only ever grows with a kind whose producer
// provably has no caller (see the design's 系统作业): a kind reachable from an HTTP
// request would hand that request an unchecked submit.
var systemJobKinds = []string{SystemJobUpgrade, SystemJobCleanup}

// SystemJobKinds returns the registered exemption list, sorted for stable reporting.
func SystemJobKinds() []string {
	out := slices.Clone(systemJobKinds)
	slices.Sort(out)
	return out
}

// IsSystemJobKind reports whether kind is a registered system job.
func IsSystemJobKind(kind string) bool {
	return slices.Contains(systemJobKinds, kind)
}

// ceilingOwners returns every caller whose ceiling bounds this request: the request's own
// origin caller, plus the extra owners a derived dispatch carries (a plan item's AUTHOR,
// next to the caller that dispatched it — design decision 8: both are checked). Empty
// values are dropped (they are the "no caller" state, not a caller) and duplicates are
// collapsed, so an owner is never judged twice.
func ceilingOwners(req JobRequest) []string {
	var out []string
	add := func(id string) {
		if id == "" || slices.Contains(out, id) {
			return
		}
		out = append(out, id)
	}
	add(req.OriginCaller)
	for _, id := range req.ExtraCeilingOwners {
		add(id)
	}
	return out
}

// CredentialCeilingOwner resolves the ceiling owner of a request authenticated with a JOB
// credential: the owner of the job that credential belongs to (a job's agent submits as
// the job, so it inherits the job's own bound — design §上限所有者).
//
// It is FAIL CLOSED: a job row that cannot be read (gone, or a store error) refuses the
// submit. Answering "" there would turn a restricted caller's derived chain into a system
// job and hand it an unchecked submit.
func (s *Service) CredentialCeilingOwner(jobID string) (string, error) {
	rec, ok, err := s.meta.GetJob(jobID)
	if err != nil {
		return "", fmt.Errorf("%w: cannot determine the ceiling owner of job credential %q: %v",
			ErrCeilingDenied, jobID, err)
	}
	if !ok {
		return "", fmt.Errorf("%w: cannot determine the ceiling owner: job %q is gone",
			ErrCeilingDenied, jobID)
	}
	return rec.OriginCaller, nil
}

// inheritCeilingOwner returns the ceiling owner a derived submission inherits from its
// source job. src is nil when the source row could not be read (pruned, cleaned up).
//
// A missing source is FAIL CLOSED for a restricted chain: if the caller the chain still
// names carries a ceiling, the derivative is refused rather than admitted as a system job.
// An unrestricted (or unnamed) caller keeps the previous behaviour — a derived job of an
// unknown origin that nobody restricted.
func inheritCeilingOwner(cfg *config.Config, src *JobResult, chainCaller string) (string, error) {
	if src != nil {
		return src.OriginCaller, nil
	}
	if chainCaller != "" && cfg != nil {
		if _, ok := cfg.Server.CallerCeiling(chainCaller); ok {
			return "", fmt.Errorf("%w: cannot determine the ceiling owner: the source job of this derivative is gone and caller %q is restricted",
				ErrCeilingDenied, chainCaller)
		}
	}
	return "", nil
}
