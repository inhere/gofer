package serve

import (
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/mail"
)

// mailResidentHost adapts job.Service to the mailbox's resident-session seam (设计 2026-10-22
// mail-agent-address「D3」): a resident agent is an ordinary continuous ACP session job on this
// machine, so the mailbox never sees the job types (G022).
type mailResidentHost struct {
	jobs   *job.Service
	agents *agent.Registry
}

// residentTurnTimeoutSec bounds ONE resident turn; the session's own idle timeout
// (mail.agents.<name>.idle_minutes) is the end of the whole session.
const residentTurnTimeoutSec = 30 * 60

func (h mailResidentHost) StartSession(sp mail.ResidentSpec) (string, error) {
	runner := strings.TrimSpace(sp.Runner)
	if runner == "" {
		runner = config.BuiltinLocalRunner
	}
	shared := false
	// gofer-81b4: the gate is resolved by the machine that RUNS the job (it is part of the
	// job's own request), and a worker dispatch does not carry it yet (the wire frame is the
	// worker protocol's business). A session configured with its own gate therefore must not
	// start on a worker: there it would be answered by that worker's project policy, which
	// can be "allow everything" - the very thing the gate was set to prevent. Fail closed;
	// the start failure is recorded and the letter waits / goes to the inbox as usual.
	if sp.Approval != nil && config.NormalizeRunnerName(runner) != config.BuiltinLocalRunner {
		return "", fmt.Errorf("mail agent %q sets its own approval policy, which a worker runner (%q) cannot enforce yet: run it on the server's local runner or drop the approval", sp.Name, runner)
	}
	req := job.JobRequest{
		ProjectKey: sp.Project, Agent: sp.Agent, Runner: runner, Cwd: ".",
		Prompt: sp.Prompt, Title: sp.Title, AgentArgs: sp.Args,
		Session: true, IdleTimeoutSec: sp.IdleSec, TimeoutSec: residentTurnTimeoutSec,
		// gofer-81b4: the resident 收件会话's own approval gate (config
		// mail.agents.<名字>.approval), which REPLACES the project's policy for this job only.
		SessionApproval: sp.Approval,
		// A resident session lives for hours: it must not hold the project's directory lock,
		// which would block every other job of that project (设计「常驻」/ the acp session lock).
		ExclusiveDir: &shared,
		Channel:      "mail", CallerID: sp.CallerID, OriginCaller: sp.OriginCaller,
	}
	if sid := strings.TrimSpace(sp.ResumeSessionID); sid != "" && sp.ResumeFromJobID != "" &&
		h.canResume(sp.Agent) {
		// A continuation LOADS the conversation: ResumedFrom + SessionID is exactly what the
		// executor reads (job.resumeLoadSessionID).
		req.SessionID = sid
		req.ResumedFrom = sp.ResumeFromJobID
	}
	res, err := h.jobs.Submit(req)
	if err != nil {
		return "", err
	}
	return res.ID, nil
}

// canResume reports whether the agent declares loadSession (an agent that cannot load its own
// past session starts fresh).
func (h mailResidentHost) canResume(agentKey string) bool {
	if h.agents == nil {
		return false
	}
	ac, ok := h.agents.Get(agentKey)
	if !ok {
		return false
	}
	return agent.ResumeCapabilities(agentKey, ac).LoadSession
}

func (h mailResidentHost) Job(jobID string) (mail.ResidentJob, bool) {
	r, ok := h.jobs.Get(jobID)
	if !ok {
		return mail.ResidentJob{}, false
	}
	return mail.ResidentJob{
		ID: r.ID, Agent: r.Agent, SessionID: r.SessionID, Status: r.Status,
		Live:          !job.IsTerminal(r.Status),
		AwaitingInput: r.Status == job.StatusAwaitingInput && !r.SessionEnding,
	}, true
}

func (h mailResidentHost) End(jobID string) error {
	err := h.jobs.EndSession(jobID)
	if errors.Is(err, job.ErrJobNotRunning) || errors.Is(err, job.ErrJobNotFound) {
		return nil
	}
	return err
}

// mailResidents maps the `mail:` block onto the mailbox's resident configuration.
func mailResidents(m config.MailConfig) mail.ResidentsConfig {
	cfg := mail.ResidentsConfig{Max: m.EffectiveAgentsMaxResident(), Agents: map[string]mail.ResidentAgentSpec{}}
	for name, a := range m.Agents {
		cfg.Agents[strings.ToLower(strings.TrimSpace(name))] = mail.ResidentAgentSpec{
			Name: strings.TrimSpace(name), Agent: a.Identity(), Args: a.Args, Runner: strings.TrimSpace(a.Runner),
			Idle: a.EffectiveIdle(), MaxLetters: a.EffectiveMaxLetters(), MaxHours: a.EffectiveMaxHours(),
			Prompt: a.Prompt, Approval: cloneApproval(a.Approval),
		}
	}
	return cfg
}

// cloneApproval copies a configured approval block, so the mailbox's snapshot cannot be
// mutated through the config it was read from (the spec outlives a reload).
func cloneApproval(a *config.ApprovalConfig) *config.ApprovalConfig {
	if a == nil {
		return nil
	}
	cp := *a
	cp.AutoAllowKinds = append([]string(nil), a.AutoAllowKinds...)
	cp.AskKinds = append([]string(nil), a.AskKinds...)
	return &cp
}
