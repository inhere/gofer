package seat

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/workbench"
)

// Why a seat cannot be resumed by hand (OpenPlan.Reason).
const (
	ReasonVacant           = "vacant"
	ReasonUnknownAgent     = "unknown_agent"
	ReasonNoResumeTemplate = "no_resume_template"
	ReasonNoProject        = "no_project"
	ReasonNeedsRestart     = "needs_restart"
)

// OpenPlan is what a person needs to get back into a seat: where to run what.
type OpenPlan struct {
	SeatID string
	Kind   string
	// Runner is the machine the sitting session registered on ("" = unknown / vacant): the
	// command is typed there.
	Runner string
	// Cwd is the directory the command is typed in: the sitting session's own reported cwd (the
	// path as the machine that runs the session sees it — a container worker reports the container
	// spelling), else the seat's directory on the host (project host path + rel_cwd).
	Cwd string
	// Resumable is true when Sh / PowerShell continue the sitting session.
	Resumable bool
	// Alive: the sitting terminal session is neither ended nor offline, so resuming it would
	// run two copies of one conversation.
	Alive bool
	// Reason says why not Resumable (the Reason* codes).
	Reason string
	// Sh / PowerShell are the resume command in each shell (Resumable only).
	Sh, PowerShell string
	// NewSh / NewPowerShell start a fresh session of the seat's agent in Cwd.
	NewSh, NewPowerShell string
	// ThreadID is the workbench thread of an ACP seat ("" for a terminal seat or a vacant one).
	ThreadID string
	// NeedsRestart: the ACP session's job ended and has to be started again; Restarted: it was,
	// and JobID is the new job.
	NeedsRestart, Restarted bool
	JobID                   string
}

// OpenPlan builds the plan for opening a seat. It does NOT reuse the takeover preview of the
// session relay: that one prepares a gofer-run pty job (global args, takeover preconditions,
// the server's own path view), while this renders what a person types in their own terminal.
func (s *Service) OpenPlan(id string) (OpenPlan, error) {
	x, err := s.Get(id)
	if err != nil {
		return OpenPlan{}, err
	}
	plan := OpenPlan{SeatID: x.ID, Kind: x.Kind}
	if s.d.Projects != nil {
		if host, _, ok := s.d.Projects.Paths(x.ProjectKey); ok && host != "" {
			plan.Cwd = joinHostPath(host, x.RelCwd)
		}
	}
	if x.Kind == jobstore.SeatKindACP {
		return s.openACP(x, plan)
	}
	agentKey, sessionID := x.Agent, ""
	if x.CurrentRef != "" {
		if a, ok, err := s.store.GetAgentSession(x.CurrentRef); err != nil {
			return OpenPlan{}, err
		} else if ok {
			plan.Runner, sessionID = a.Runner, a.SessionID
			plan.Alive = a.State != jobstore.SessionEnded && a.State != jobstore.SessionOffline
			// The session reports the directory in the spelling of its own machine, so a seat on a
			// container worker is opened with the container path, not the host one.
			if cwd := strings.TrimSpace(a.Cwd); cwd != "" {
				plan.Cwd = cwd
			}
			if agentKey == "" {
				agentKey = a.Agent
			}
		}
	}
	var ac agentCfg
	if s.d.Agents != nil && agentKey != "" {
		ac.cfg, ac.ok = s.d.Agents.Get(agentKey)
	}
	if ac.ok {
		if argv := agent.InteractiveNewArgv(ac.cfg); argv != nil {
			plan.NewSh, plan.NewPowerShell = shellLine(plan.Cwd, argv, false), shellLine(plan.Cwd, argv, true)
		}
	}
	switch {
	case sessionID == "":
		plan.Reason = ReasonVacant
	case !ac.ok:
		plan.Reason = ReasonUnknownAgent
	case plan.Cwd == "":
		plan.Reason = ReasonNoProject
	default:
		argv := agent.InteractiveResumeArgv(ac.cfg, sessionID)
		if argv == nil {
			plan.Reason = ReasonNoResumeTemplate
			break
		}
		plan.Resumable = true
		plan.Sh, plan.PowerShell = shellLine(plan.Cwd, argv, false), shellLine(plan.Cwd, argv, true)
	}
	return plan, nil
}

type agentCfg struct {
	cfg config.AgentConfig
	ok  bool
}

func (s *Service) openACP(x jobstore.Seat, plan OpenPlan) (OpenPlan, error) {
	if x.CurrentRef == "" {
		plan.Reason = ReasonVacant
		return plan, nil
	}
	rec, ok, err := s.store.GetJob(x.CurrentRef)
	if err != nil {
		return OpenPlan{}, err
	}
	if !ok {
		plan.Reason = ReasonVacant
		return plan, nil
	}
	plan.Runner = rec.Runner
	plan.ThreadID = workbench.JobThreadID(rec.ID, rec.SessionID)
	if !job.IsTerminal(rec.Status) {
		plan.Resumable = true
		return plan, nil
	}
	plan.NeedsRestart, plan.Reason = true, ReasonNeedsRestart
	return plan, nil
}

// Restart starts the ended ACP session of an acp seat again, from the original session id, and
// moves the seat to the new job. It is the only part of opening a seat that has a side effect
// (it submits a job), so it is separate from OpenPlan, which only reads. A seat that is not an
// acp seat, is vacant, or whose job still runs is jobstore.ErrSeatInvalid.
func (s *Service) Restart(ctx context.Context, id string) (OpenPlan, error) {
	x, err := s.Get(id)
	if err != nil {
		return OpenPlan{}, err
	}
	plan, err := s.OpenPlan(id)
	if err != nil {
		return OpenPlan{}, err
	}
	if x.Kind != jobstore.SeatKindACP || !plan.NeedsRestart {
		return OpenPlan{}, fmt.Errorf("%w: seat %s has no ended session to restart", jobstore.ErrSeatInvalid, x.ID)
	}
	rec, _, err := s.store.GetJob(x.CurrentRef)
	if err != nil {
		return OpenPlan{}, err
	}
	if s.d.Restarter == nil {
		return OpenPlan{}, fmt.Errorf("seat: this server cannot restart ACP sessions")
	}
	agentKey := x.Agent
	if agentKey == "" {
		agentKey = rec.Agent
	}
	jobID, err := s.d.Restarter.RestartACP(ctx, RestartRequest{
		ProjectKey: x.ProjectKey, Agent: agentKey, Runner: rec.Runner, RelCwd: x.RelCwd,
		SessionID: rec.SessionID, FromJobID: rec.ID, CallerID: x.CallerID,
		Prompt: s.restartPrompt(x, agentKey),
	})
	if err != nil {
		return OpenPlan{}, err
	}
	if _, err := s.store.BindSeat(x.ID, jobID, true, "system"); err != nil {
		return OpenPlan{}, err
	}
	plan.Restarted, plan.NeedsRestart, plan.Reason, plan.Resumable, plan.JobID = true, false, "", true, jobID
	return plan, nil
}

// restartPrompt is the opening input of a restarted ACP session: when the agent loads the old
// conversation only the charter is repeated, otherwise the session is new and gets the whole
// continuation text.
func (s *Service) restartPrompt(x jobstore.Seat, agentKey string) string {
	if s.d.Agents != nil {
		if ac, ok := s.d.Agents.Get(agentKey); ok && agent.ResumeCapabilities(agentKey, ac).LoadSession {
			return s.charterReminder(x)
		}
	}
	text, err := s.ContinuationText(x)
	if err != nil {
		return s.charterReminder(x)
	}
	return text
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func quoteSh(a string) string {
	if shellSafe.MatchString(a) {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

func quotePS(a string) string {
	if shellSafe.MatchString(a) {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", "''") + "'"
}

// shellLine renders `cd <dir> && cmd args` (sh) or `Set-Location <dir>; cmd args` (PowerShell).
func shellLine(dir string, argv []string, ps bool) string {
	quote, join := quoteSh, " && "
	cd := "cd "
	if ps {
		quote, join, cd = quotePS, "; ", "Set-Location "
	}
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		parts = append(parts, quote(a))
	}
	cmd := strings.Join(parts, " ")
	if dir == "" {
		return cmd
	}
	return cd + quote(dir) + join + cmd
}

// joinHostPath appends the project-relative directory to the host root, keeping the root's own
// separator style (a Windows host path stays backslashed).
func joinHostPath(root, rel string) string {
	if rel == "" || rel == "." {
		return root
	}
	if strings.Contains(root, `\`) {
		return strings.TrimRight(root, `\/`) + `\` + strings.ReplaceAll(rel, "/", `\`)
	}
	return strings.TrimRight(root, "/") + "/" + rel
}
