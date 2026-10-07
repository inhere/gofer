package sessionrelay

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	// Message is the plain-language (Chinese) explanation: why not, or what will happen.
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
	// CwdAbs is Cwd resolved against the runner's project root; CwdSource is
	// transcript | registered | project_root, and CwdReason says in plain Chinese
	// why that directory was chosen (M8: the original start directory is verified
	// from the transcript path, see ChooseResumeCwd).
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
	tp, choice, cerr := s.takeoverCheck(a)
	if cerr != nil {
		plan.Reason = DeliverReason(cerr)
		plan.Message = ExplainReason(plan.Reason, a, cerr)
		return plan
	}
	cwd := choice.Rel
	plan.Can, plan.Cwd, plan.Command = true, cwd, append([]string(nil), tp.Argv...)
	plan.CwdAbs, plan.CwdSource, plan.CwdReason = choice.Abs, choice.Source, choice.Reason
	if plan.Ended {
		plan.Message = fmt.Sprintf("会话已结束。将在执行机 %s 上起一个新进程，用「%s」继续这个会话，目录 %s。依据：%s。", runnerLabel(a.Runner), a.Agent, choice.Abs, choice.Reason)
	} else {
		plan.Message = fmt.Sprintf("将在执行机 %s 上起一个新进程，用「%s」接管这个会话，目录 %s。依据：%s。", runnerLabel(a.Runner), a.Agent, choice.Abs, choice.Reason)
		plan.Warning = "这个会话还没有标记为结束，原来的终端可能仍开着；两个进程同时写同一个会话会互相覆盖，建议先关掉原终端。"
		if a.State == jobstore.SessionOffline {
			plan.Message = fmt.Sprintf("会话长时间没有心跳，已标记为离线（原进程可能已退出）。将在执行机 %s 上起一个新进程，用「%s」接管这个会话，目录 %s。依据：%s。", runnerLabel(a.Runner), a.Agent, choice.Abs, choice.Reason)
			plan.Warning = "原进程可能已退出；若它其实还开着，两个进程同时写同一个会话会互相覆盖，建议先确认原终端已关闭。"
		}
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
// wrapped error is the Chinese explanation.
func (s *Service) Resume(ctx context.Context, sid, initialInput, by string) (DeliverResult, error) {
	if len(initialInput) > MaxDeliverText {
		return DeliverResult{}, fmt.Errorf("%w: text is %d bytes, limit %d", ErrInvalidInput, len(initialInput), MaxDeliverText)
	}
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil {
		return DeliverResult{}, err
	}
	if !ok {
		return DeliverResult{}, ErrUnknownSession
	}
	if err := s.takeoverAliveCheck(a, false); err != nil {
		return DeliverResult{}, &UndeliverableError{Reason: ReasonSessionAlive, Err: errors.New(ExplainReason(ReasonSessionAlive, a, err))}
	}
	res, err := s.deliverTakeover(ctx, a, initialInput, by)
	if err != nil {
		if reason := DeliverReason(err); reason != "" {
			return DeliverResult{}, &UndeliverableError{Reason: reason, Err: errors.New(ExplainReason(reason, a, err))}
		}
		return DeliverResult{}, err
	}
	return res, nil
}

func runnerLabel(r string) string {
	if strings.TrimSpace(r) == "" {
		return "(未登记)"
	}
	return r
}

// ExplainReason turns a delivery/takeover failure code into a plain Chinese
// sentence for the web. cause is the original error (its text is only quoted for
// codes that carry no other information, such as a runner-side failure).
func ExplainReason(reason string, a jobstore.AgentSession, cause error) string {
	switch {
	case reason == ReasonNoRunner:
		return "这个会话没有登记执行机（或这台 server 没有接入任务执行器），无法在它原来的机器上起新进程。"
	case strings.HasPrefix(reason, HandedOffPrefix):
		return fmt.Sprintf("这个会话已经被作业 %s 接管，正在运行。请直接打开那个作业继续对话；要重新起一个，先释放接管。", strings.TrimPrefix(reason, HandedOffPrefix))
	case reason == ReasonNoResumeTemplate:
		return fmt.Sprintf("会话所属的 agent「%s」没有配置交互式续接模板（session_resume_interactive），gofer 不知道怎么用新进程继续它。", a.Agent)
	case reason == ReasonInteractiveNotAllowed:
		return fmt.Sprintf("项目「%s」没有开启交互终端（allow_interactive），gofer 不会为它起终端进程。需要的话在项目配置里打开。", a.ProjectKey)
	case reason == ReasonCwdOutsideProject:
		return fmt.Sprintf("会话的目录 %s 不在项目「%s」的根目录之下（按这台执行机看到的路径），在那里起新进程会落到错误的目录。", a.Cwd, a.ProjectKey)
	case strings.HasPrefix(reason, InjectFailedPrefix):
		msg := "执行机没能启动新进程"
		if cause != nil {
			var ue *UndeliverableError
			if errors.As(cause, &ue) && ue.Err != nil {
				msg += "：" + ue.Err.Error()
			}
		}
		return msg
	case reason == ReasonSessionAlive:
		return "会话进程仍在线（刚刚还有心跳），为避免两个进程同时写同一个会话，已拒绝接管。请在原终端继续，或用在线送话。"
	case reason == ReasonNotRunning:
		return "会话进程已不在运行（送话命令报告没有存活进程）。"
	case strings.HasPrefix(reason, DeliverFailedPrefix):
		return "agent 的送话命令失败：" + strings.TrimPrefix(reason, DeliverFailedPrefix)
	case reason == ReasonEnded:
		return "会话已结束。"
	default:
		return "无法唤醒这个会话（" + reason + "）。"
	}
}
