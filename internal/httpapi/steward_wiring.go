package httpapi

import (
	"errors"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
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
	// lang is the language of the reasons CheckAgent gives: the steward settings
	// answer has no request behind it here, so it is the server's (nil = default).
	lang func() i18n.Lang
}

func (h stewardHost) text(key string, args ...any) string {
	var lang i18n.Lang
	if h.lang != nil {
		lang = h.lang()
	}
	return i18n.T(lang, key, args...)
}

// stewardTurnTimeoutSec bounds ONE steward turn (the session's idle end is separate).
const stewardTurnTimeoutSec = 20 * 60

func (h stewardHost) StartSession(sp steward.StartSpec) (string, error) {
	shared := false
	approval := sp.Approval
	if approval == nil {
		pol := config.StewardApprovalOver(h.projectApproval(sp.Project))
		approval = &pol
	}
	res, err := h.jobs.Submit(job.JobRequest{
		ProjectKey: sp.Project, Agent: sp.Agent, Runner: config.BuiltinLocalRunner, Cwd: ".",
		Prompt: sp.Prompt, Title: sp.Title, Tags: []string{job.StewardTag},
		Session: true, IdleTimeoutSec: sp.IdleSec, TimeoutSec: stewardTurnTimeoutSec,
		// The steward reads and writes through gofer, never in the workspace: it must not
		// hold the directory lock other jobs of the default project need.
		ExclusiveDir: &shared,
		Channel:      "steward", CallerID: "gofer",
		// gofer-j0aq: the steward's own approval gate — steward.approval when configured,
		// otherwise the project's policy with the steward's writes released. It REPLACES the project's policy for this one
		// job, so the steward's gofer writes run without a human while every other ACP
		// session of the project keeps asking — and the gate's own protections (gofer's
		// MCP attribution, the mutating kinds, mode=strict) still apply.
		SessionApproval: approval,
		Review:          false, ReviewFixed: true,
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
		return errors.New(h.text("err.steward_no_registry"))
	}
	ac, ok := h.agents.Get(key)
	if !ok {
		return errors.New(h.text("err.steward_agent_missing", "agent", key))
	}
	if ac.Type != agent.TypeACPAgent {
		return errors.New(h.text("err.steward_agent_not_acp", "agent", key))
	}
	if av, found := h.agents.Availability()[key]; found && !av.Available {
		why := av.Error
		if why == "" {
			why = h.text("err.agent_command_not_found")
		}
		return errors.New(h.text("err.steward_agent_unavailable", "agent", key, "why", why))
	}
	return nil
}

// projectApproval is the project's own approval policy (the default when the project is
// unknown or there is no config), the base a steward gate is derived from.
func (h stewardHost) projectApproval(project string) config.ApprovalConfig {
	if h.jobs != nil {
		if cfg := h.jobs.Config(); cfg != nil {
			if p, ok := cfg.Projects[project]; ok {
				return p.ApprovalPolicy()
			}
		}
	}
	return config.ProjectConfig{}.ApprovalPolicy()
}
