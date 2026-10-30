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
	"log/slog"
	"slices"

	"github.com/inhere/gofer/internal/agent"
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

// Internal exec kinds: jobs that run a command gofer itself composed (not one the owner chose)
// on behalf of a caller. They are NOT system jobs: they carry the triggering caller's (or the
// source job's) ceiling owner and are judged on project, runner and worker like any other
// job of that owner. Only the owner's exec dimension and the exec carrier's agent entry are
// waived, because the owner cannot choose the command: allow_exec guards "run my command",
// and a delivery / relay / sync job runs gofer's own. The list is closed like systemJobKinds.
const (
	// InternalExecDelivery is `gofer tool deliver`, run on the worker that holds a job's
	// worktree (deliverRemote). Owner: the delivered job's origin caller.
	InternalExecDelivery = "delivery"
	// InternalExecRelay is a session-relay job: the inject script, the agent's own
	// deliver_command, a takeover's resume argv, the one-shot messenger. Owner: the caller
	// whose request caused the message.
	InternalExecRelay = "relay"
	// InternalExecTrackerSync is `gofer repo sync` for a mirrored tracker repo. Owner: the
	// caller that asked for the sync.
	InternalExecTrackerSync = "tracker-sync"
	// InternalExecSummarizer is the work summarizer's read-only one-shot run (agent argv
	// from the work configuration). Owner: the caller that asked for the tidy-up.
	InternalExecSummarizer = "summarizer"
	// InternalExecDeliveryCheck is the read-only one-shot run of the delivery checker
	// (agent argv from server.delivery_check). Owner: the caller that caused the job
	// being checked.
	InternalExecDeliveryCheck = "delivery-check"
	// InternalExecWorkflowStep is `gofer tool step <name>`: a built-in workflow step
	// (`uses: gofer/<name>`) the workflow engine starts on the step's runner. Owner: the
	// caller that submitted the workflow (its project / runner / worker ceilings apply).
	InternalExecWorkflowStep = "workflow-step"
)

var internalExecKinds = []string{InternalExecDelivery, InternalExecRelay, InternalExecTrackerSync, InternalExecSummarizer, InternalExecDeliveryCheck, InternalExecWorkflowStep}

// InternalExecKinds returns the registered internal exec kinds, sorted.
func InternalExecKinds() []string {
	out := slices.Clone(internalExecKinds)
	slices.Sort(out)
	return out
}

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

// admitCeiling enforces every caller ceiling that bounds this request — the request's
// own ceiling owner plus every extra owner it carries (design 2026-10-18 §准入, decision
// 8) — against the five dimensions a ceiling declares: project, runner, routed worker,
// agent and exec. A caller with no ceiling (and a request with no owner at all, i.e. a
// system job) is admitted exactly as before.
//
// It is called from the single submit entry point BEFORE any side effect and again from
// validate, so every path (HTTP, timer, plan item, retry, approval, internal job) is
// covered by one implementation. The refusal is PERMANENT (ErrCeilingDenied).
func admitCeiling(cfg *config.Config, req JobRequest) error {
	if cfg == nil {
		return nil
	}
	if req.SystemJob != "" {
		// The exemption list is closed and server-set: an unknown kind is refused, and so is
		// a registered one that carries an owner (no caller's chain may dress itself up as
		// gofer's own work).
		if !IsSystemJobKind(req.SystemJob) {
			return fmt.Errorf("%w: unknown system job kind %q", ErrCeilingDenied, req.SystemJob)
		}
		if len(ceilingOwners(req)) > 0 {
			return fmt.Errorf("%w: system job %q cannot carry a ceiling owner", ErrCeilingDenied, req.SystemJob)
		}
		return nil
	}
	if req.InternalExec != "" && !slices.Contains(internalExecKinds, req.InternalExec) {
		return fmt.Errorf("%w: unknown internal exec kind %q", ErrCeilingDenied, req.InternalExec)
	}
	for _, owner := range ceilingOwners(req) {
		ceil, ok := cfg.Server.CallerCeiling(owner)
		if !ok {
			continue
		}
		if err := checkCeilingOwner(cfg, req, owner, ceil); err != nil {
			return err
		}
	}
	return nil
}

// checkCeilingOwner judges ONE owner's ceiling against a request.
func checkCeilingOwner(cfg *config.Config, req JobRequest, owner string, ceil *config.CallerCeiling) error {
	deny := func(dim, detail string) error {
		return fmt.Errorf("%w: caller %q: %s %s", ErrCeilingDenied, owner, dim, detail)
	}
	if !ceil.AllowsProject(req.ProjectKey) {
		return deny("project", fmt.Sprintf("%q is not in the caller's ceiling", req.ProjectKey))
	}
	if !ceil.AllowsRunner(req.Runner) {
		return deny("runner", fmt.Sprintf("%q is not in the caller's ceiling", req.Runner))
	}
	// The ROUTED worker: an explicit worker_id, else the runner's pin. A label auto-select
	// is judged by selectTargetWorker once the worker is picked.
	if isWorkerRunner(cfg, req.Runner) {
		if w := targetWorkerSegment(cfg, req); w != "" && !ceil.AllowsWorker(w) {
			return deny("worker", fmt.Sprintf("%q is not in the caller's ceiling", w))
		}
	}
	// gateAgentOf: a resume submission executes as the built-in exec carrier but its real
	// identity is the SOURCE agent whose session it continues.
	agentKey := gateAgentOf(req)
	// gofer's own exec carrier is not the owner's agent choice (InternalExec*); a real
	// agent (a session's source agent, the summarizer's cli-agent) is still judged.
	internal := req.InternalExec != ""
	if agentKey != "" && !(internal && agentKey == agent.ExecAgentKey) && !ceil.AllowsAgent(agentKey) {
		return deny("agent", fmt.Sprintf("%q is not in the caller's ceiling", agentKey))
	}
	if !ceil.ExecAllowed() && !internal {
		// Every form of "this request runs a command of the caller's choosing", independent
		// of whether the runner is remote (the design's exec dimension). A resume carries the
		// SOURCE agent, so continuing a cli-agent session is not an exec job.
		switch {
		case gateAgentOf(req) == agent.ExecAgentKey:
			return deny("exec", "the caller may not run exec jobs")
		case len(req.Cmd) > 0:
			return deny("exec", "the caller may not submit a cmd")
		case len(req.Verify) > 0:
			return deny("exec", "the caller may not submit a verify step")
		case len(req.AgentArgs) > 0:
			return deny("exec", "the caller may not submit agent argv")
		}
	}
	return nil
}

// checkCeilingWorker re-judges the worker dimension once the ROUTED worker is known: a
// label auto-select can pick a worker the request never named, and a ceiling that lists
// workers must bound the one the job actually lands on.
func checkCeilingWorker(cfg *config.Config, req JobRequest) error {
	if cfg == nil || req.WorkerID == "" {
		return nil
	}
	for _, owner := range ceilingOwners(req) {
		ceil, ok := cfg.Server.CallerCeiling(owner)
		if !ok {
			continue
		}
		if !ceil.AllowsWorker(req.WorkerID) {
			return fmt.Errorf("%w: caller %q: worker %q is not in the caller's ceiling",
				ErrCeilingDenied, owner, req.WorkerID)
		}
	}
	return nil
}

// recordCeilingDenied records the PERMANENT refusal of a submission gofer started on a
// job's behalf (design 2026-10-18 §准入: 自动续投、失败转移、wakeup … 同样终止并记录). jobID is the job
// whose chain the refused submission belonged to, path names the re-submission path and
// err is the admission error — the ceiling will not change by waiting, so this row is
// the only trace of why the automatic follow-up stopped (the job's terminal event
// carries its own, original failure instead).
func (s *Service) recordCeilingDenied(jobID, path string, err error) {
	reason := err.Error()
	slog.Warn("ceiling denied a derived submission", "job_id", jobID, "path", path, "reason", reason)
	s.recordEvent(jobID, EventJobCeilingDenied, map[string]any{"path": path, "reason": reason})
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
