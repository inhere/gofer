package httpapi

import (
	"errors"
	"fmt"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/steward"
)

// stewardHost adapts job.Service to the steward's session seam: the steward is an ordinary
// resident ACP session job on the server's own machine, stamped (Steward) so it gets the
// steward credential and the injected gofer MCP. The steward package never sees the job
// types (G022).
type stewardHost struct {
	jobs   *job.Service
	agents *agent.Registry
}

// stewardTurnTimeoutSec bounds ONE steward turn (the session's idle end is separate).
const stewardTurnTimeoutSec = 20 * 60

func (h stewardHost) StartSession(sp steward.StartSpec) (string, error) {
	shared := false
	res, err := h.jobs.Submit(job.JobRequest{
		ProjectKey: sp.Project, Agent: sp.Agent, Runner: config.BuiltinLocalRunner, Cwd: ".",
		Prompt: sp.Prompt, Title: sp.Title, Tags: []string{job.StewardTag},
		Session: true, IdleTimeoutSec: sp.IdleSec, TimeoutSec: stewardTurnTimeoutSec,
		// The steward reads and writes through gofer, never in the workspace: it must not
		// hold the directory lock other jobs of the default project need.
		ExclusiveDir: &shared,
		Channel:      "steward", CallerID: "gofer",
		Review: false, ReviewFixed: true,
		Steward: true,
	})
	if err != nil {
		return "", err
	}
	return res.ID, nil
}

func (h stewardHost) Say(jobID, text string) error { return h.jobs.SaySession(jobID, text) }

func (h stewardHost) End(jobID string) error {
	err := h.jobs.EndSession(jobID)
	if errors.Is(err, job.ErrJobNotRunning) || errors.Is(err, job.ErrJobNotFound) {
		return nil // already gone: nothing to end
	}
	return err
}

func (h stewardHost) Job(jobID string) (steward.JobInfo, bool) {
	r, ok := h.jobs.Get(jobID)
	if !ok {
		return steward.JobInfo{}, false
	}
	return steward.JobInfo{
		ID: r.ID, Agent: r.Agent, Status: r.Status, Live: !job.IsTerminal(r.Status),
		AwaitingInput: r.Status == job.StatusAwaitingInput && !r.SessionEnding,
		TurnNo:        r.TurnNo, StartedAt: r.StartedAt,
	}, true
}

// CheckAgent says why an agent cannot run as steward: it must be a configured acp-agent
// (a resident session needs the ACP protocol) and installed on this machine.
func (h stewardHost) CheckAgent(key string) error {
	if h.agents == nil {
		return errors.New("没有 agent 注册表")
	}
	ac, ok := h.agents.Get(key)
	if !ok {
		return fmt.Errorf("agent %q 不存在（在 steward.agent 里改成已安装的 acp-agent）", key)
	}
	if ac.Type != agent.TypeACPAgent {
		return fmt.Errorf("agent %q 不是 acp-agent（管家需要常驻的 ACP 会话）", key)
	}
	if av, found := h.agents.Availability()[key]; found && !av.Available {
		why := av.Error
		if why == "" {
			why = "本机没有找到该命令"
		}
		return fmt.Errorf("agent %q 不可用：%s", key, why)
	}
	return nil
}
