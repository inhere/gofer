package sessionrelay

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// ResumePlan answers "can this session be woken up, and how" for the web's wake-up
// button. It is the dry run of Resume: the same preconditions as path B, none of
// the side effects.
type ResumePlan struct {
	// Can is true when Resume would start a process now.
	Can bool `json:"can"`
	// Reason is the machine-readable code when !Can (see the Reason* constants and
	// HandedOffPrefix); "" when Can.
	Reason string `json:"reason,omitempty"`
	// Message is the plain-language explanation: why not, or what will happen.
	Message string `json:"message"`
	// Warning is non-empty when waking is possible but worth a second look (the
	// terminal may still be open).
	Warning string `json:"warning,omitempty"`
	State   string `json:"state"`
	Ended   bool   `json:"ended"`
	// What would run: the machine, the agent, the project, the project-relative
	// directory and the resume command line. Empty when it could not be planned.
	Runner     string   `json:"runner,omitempty"`
	Agent      string   `json:"agent,omitempty"`
	ProjectKey string   `json:"project_key,omitempty"`
	Cwd        string   `json:"cwd,omitempty"`
	Command    []string `json:"command,omitempty"`
	// CwdAbs is Cwd resolved against the runner's project root. CwdSource is
	// transcript | registered | project_root, and CwdReason says in
	// plain language why that directory was chosen (M8: the original start
	// directory is verified from the transcript path, see ChooseResumeCwd).
	CwdAbs    string `json:"cwd_abs,omitempty"`
	CwdSource string `json:"cwd_source,omitempty"`
	CwdReason string `json:"cwd_reason,omitempty"`
}

// PlanResume reports whether Resume would work for sid, without doing it. An
// unknown session is ErrUnknownSession; every other "no" is a normal answer
// (Can=false with a reason), not an error.
func (s *Service) PlanResume(sid string) (ResumePlan, error) {
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil {
		return ResumePlan{}, err
	}
	if !ok {
		return ResumePlan{}, ErrUnknownSession
	}
	return s.PlanResumeFor(a), nil
}

// PlanResumeFor is PlanResume for a session row already in hand (the session list
// annotates every row with it, so it must not touch the store).
func (s *Service) PlanResumeFor(a jobstore.AgentSession) ResumePlan {
	plan := ResumePlan{State: a.State, Ended: a.State == jobstore.SessionEnded,
		Runner: a.Runner, Agent: a.Agent, ProjectKey: a.ProjectKey}
	lang := s.lang(a.ProjectKey)
	tp, choice, cerr := s.takeoverCheck(a)
	if cerr != nil {
		plan.Reason = DeliverReason(cerr)
		plan.Message = ExplainReasonIn(lang, plan.Reason, a, cerr)
		return plan
	}
	cwd := choice.Rel
	plan.Can, plan.Cwd, plan.Command = true, cwd, append([]string(nil), tp.Argv...)
	plan.CwdAbs, plan.CwdSource, plan.CwdReason = choice.Abs, choice.Source, choice.Reason
	switch {
	case plan.Ended:
		plan.Message = i18n.T(lang, "sessionrelay.resume.planned_continue", "runner", runnerLabelIn(lang, a.Runner),
			"agent", a.Agent, "cwd", choice.Abs, "basis", choice.Reason)
	case a.State == jobstore.SessionOffline:
		plan.Message = i18n.T(lang, "sessionrelay.resume.planned_offline", "runner", runnerLabelIn(lang, a.Runner),
			"agent", a.Agent, "cwd", choice.Abs, "basis", choice.Reason)
		plan.Warning = i18n.T(lang, "sessionrelay.resume.warning_offline")
	default:
		plan.Message = i18n.T(lang, "sessionrelay.resume.planned_takeover", "runner", runnerLabelIn(lang, a.Runner),
			"agent", a.Agent, "cwd", choice.Abs, "basis", choice.Reason)
		plan.Warning = i18n.T(lang, "sessionrelay.resume.warning_not_ended")
	}
	return plan
}

// Resume starts a new interactive process that continues the session
// (`--resume <sid>`), whether or not the session has ended — waking a closed
// terminal is the main reason to call it. initialInput is optional: with one the
// new terminal's first input is that text, without it the terminal just opens on
// the resumed conversation. The session becomes handed_off until the process ends
// (or is released), and an ended session returns to ended then.
//
// Failures are *UndeliverableError with a reason code (see DeliverReason); their
// wrapped error is the Chinese explanation. by is the display label, origin the caller-scope
// ceiling owner of the takeover job (see ceiling_origin.go).
func (s *Service) Resume(ctx context.Context, sid, initialInput, by, origin string) (DeliverResult, error) {
	if len(initialInput) > MaxDeliverText {
		return DeliverResult{}, fmt.Errorf("%w: text is %d bytes, limit %d", ErrInvalidInput, len(initialInput), MaxDeliverText)
	}
	if _, err := s.ceilingOwner(origin, "a session takeover"); err != nil {
		return DeliverResult{}, err
	}
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil {
		return DeliverResult{}, err
	}
	if !ok {
		return DeliverResult{}, ErrUnknownSession
	}
	if err := s.takeoverAliveCheck(a, false); err != nil {
		return DeliverResult{}, &UndeliverableError{Reason: ReasonSessionAlive, Err: errors.New(ExplainReasonIn(s.lang(a.ProjectKey), ReasonSessionAlive, a, err))}
	}
	res, err := s.deliverTakeover(ctx, a, initialInput, by, origin)
	if err != nil {
		if reason := DeliverReason(err); reason != "" {
			return DeliverResult{}, &UndeliverableError{Reason: reason, Err: errors.New(ExplainReasonIn(s.lang(a.ProjectKey), reason, a, err))}
		}
		return DeliverResult{}, err
	}
	return res, nil
}

func runnerLabel(r string) string { return runnerLabelIn(i18n.Default, r) }

// runnerLabelIn names a session's execution machine in lang; an empty label is
// spelled out, because "" in the middle of a sentence reads as a mistake.
func runnerLabelIn(lang i18n.Lang, r string) string {
	if strings.TrimSpace(r) == "" {
		return i18n.T(lang, "sessionrelay.resume.runner_unknown")
	}
	return r
}

// ExplainReasonIn turns a delivery/takeover failure code into a plain sentence IN
// LANG for the web. cause is the original error (its text is only quoted for codes
// that carry no other information, such as a runner-side failure).
func ExplainReasonIn(lang i18n.Lang, reason string, a jobstore.AgentSession, cause error) string {
	switch {
	case reason == ReasonNoRunner:
		return i18n.T(lang, "sessionrelay.resume.no_runner")
	case strings.HasPrefix(reason, HandedOffPrefix):
		return i18n.T(lang, "sessionrelay.resume.handed_off", "job", strings.TrimPrefix(reason, HandedOffPrefix))
	case reason == ReasonNoResumeTemplate:
		return i18n.T(lang, "sessionrelay.resume.no_resume_template", "agent", a.Agent)
	case reason == ReasonInteractiveNotAllowed:
		return i18n.T(lang, "sessionrelay.resume.interactive_not_allowed", "project", a.ProjectKey)
	case reason == ReasonCwdOutsideProject:
		return i18n.T(lang, "sessionrelay.resume.cwd_outside_project", "cwd", a.Cwd, "project", a.ProjectKey)
	case strings.HasPrefix(reason, InjectFailedPrefix):
		msg := i18n.T(lang, "sessionrelay.resume.inject_failed")
		if cause != nil {
			var ue *UndeliverableError
			if errors.As(cause, &ue) && ue.Err != nil {
				msg += i18n.T(lang, "sessionrelay.resume.detail_sep") + ue.Err.Error()
			}
		}
		return msg
	case reason == ReasonSessionAlive:
		return i18n.T(lang, "sessionrelay.resume.session_alive")
	case reason == ReasonNotRunning:
		return i18n.T(lang, "sessionrelay.resume.not_running")
	case strings.HasPrefix(reason, DeliverFailedPrefix):
		return i18n.T(lang, "sessionrelay.resume.deliver_failed", "detail", strings.TrimPrefix(reason, DeliverFailedPrefix))
	case reason == ReasonEnded:
		return i18n.T(lang, "sessionrelay.resume.ended")
	default:
		return i18n.T(lang, "sessionrelay.resume.unknown", "reason", reason)
	}
}
