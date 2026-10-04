package httpapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/messenger"
	"github.com/inhere/gofer/internal/project"
	runnerpkg "github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/sessionrelay"
	"github.com/inhere/gofer/internal/store"
)

// injectWaitSec is how long the HTTP request waits for the injection job before
// reporting it as still running. It is deliberately BELOW the CLI client's 30s
// HTTP timeout (and below the job's own 30s deadline) so the caller always gets
// a real answer instead of a client-side timeout racing the server.
const injectWaitSec = 25

// sessionInjector adapts the job service to the relay's injection seam (design
// §9.1 A): path A's internal exec job is an ordinary job — it runs on the
// session's own runner, in its project, under its timeout — so it shows up in
// `gofer job ls --tag relay-inject` with full logs, like any other work.
//
// It exists here, in the entry/assembly layer, so the relay stays job-agnostic
// (G021/G022): the relay decides WHAT to type and reads the exit code; this type
// owns the submission vocabulary.
type sessionInjector struct {
	jobs *job.Service
	// projects / agents resolve what path B needs to plan a takeover (design §9.1
	// B): the agent's interactive resume argv, the project's allow_interactive
	// switch and the project root as the runner sees it. The relay holds neither
	// (it must not import config/agent — G022), so the questions arrive here.
	projects         *project.Registry
	agents           *agent.Registry
	resident         *messenger.Manager
	messengerTimeout time.Duration
	messengerIdle    time.Duration
}

func (x sessionInjector) SendMessengerResident(ctx context.Context, runner, cwd, target string, command []string) (string, error) {
	if x.resident == nil {
		return "", errors.New("resident messenger unavailable")
	}
	return x.resident.Send(ctx, runner, cwd, target, command)
}

// SubmitMessenger adapts the Y6 one-shot Claude SendMessage bridge to the job
// service. It deliberately uses a tagged internal exec job so ordinary job
// listings can hide/filter it without changing user job semantics.
func (x sessionInjector) SubmitMessenger(projectKey, runner, cwd string, command []string, title, caller string) (string, error) {
	timeout := x.messengerTimeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	out, err := x.jobs.Submit(job.JobRequest{
		ProjectKey: projectKey, Agent: agent.ExecAgentKey, Runner: runnerKeyForSession(runner),
		Cmd: command, Cwd: ".", Title: title, Tags: []string{job.MessengerJobTag},
		Env: map[string]string{"GOFER_MESSENGER": "1"},
		MessengerMeta: &job.MessengerMeta{
			TargetSession: strings.TrimSpace(strings.TrimPrefix(title, "session messenger · ")),
			Message:       messengerOriginalMessage(command),
			Channel:       messengerJobChannel(runner),
		},
		TimeoutSec: int(timeout / time.Second), CallerID: "", EnvDenyExtra: []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT"},
		Messenger: &runnerpkg.MessengerDispatch{
			SessionName: strings.TrimSpace(strings.TrimPrefix(title, "session messenger · ")),
			Command:     append([]string(nil), command...),
			Cwd:         cwd,
			TimeoutSec:  int(timeout / time.Second),
			IdleSec:     int(x.messengerIdle / time.Second),
		},
	})
	if err != nil {
		return "", err
	}
	return out.ID, nil
}

func messengerOriginalMessage(command []string) string {
	prompt := ""
	for i, part := range command {
		if part == "-p" && i+1 < len(command) {
			prompt = command[i+1]
			break
		}
	}
	if marker := strings.Index(prompt, "] "); marker >= 0 {
		return strings.TrimSpace(prompt[marker+2:])
	}
	return strings.TrimSpace(prompt)
}

func messengerJobChannel(runner string) string {
	if config.NormalizeRunnerName(runner) == config.BuiltinLocalRunner {
		return "one-shot"
	}
	return "resident"
}

func (x sessionInjector) MessengerJob(jobID string) (done bool, status string, exitCode int, output string, err error) {
	out, ok := x.jobs.Get(jobID)
	if !ok {
		return false, "", -1, "", errors.New("messenger job not found")
	}
	if !job.IsTerminal(out.Status) {
		return false, out.Status, out.ExitCode, "", nil
	}
	stdout, _ := x.jobs.TailLog(jobID, store.StreamStdout, 4096)
	stderr, _ := x.jobs.TailLog(jobID, store.StreamStderr, 4096)
	output = strings.TrimSpace(string(stdout))
	if strings.TrimSpace(string(stderr)) != "" {
		if output != "" {
			output += "\n"
		}
		output += strings.TrimSpace(string(stderr))
	}
	if output == "" {
		output = out.Error
	}
	return true, out.Status, out.ExitCode, output, nil
}

// InjectSession runs one injection job to completion and reports its exit code
// plus the stdout tail (the pane check writes its reason code there).
func (x sessionInjector) InjectSession(_ context.Context, req sessionrelay.InjectRequest) (sessionrelay.InjectResult, error) {
	out, async, err := x.jobs.SubmitSync(job.JobRequest{
		ProjectKey: req.ProjectKey,
		Agent:      agent.ExecAgentKey,
		Runner:     runnerKeyForSession(req.Runner),
		Cmd:        req.Cmd,
		Cwd:        req.Cwd,
		Title:      req.Title,
		Tags:       req.Tags,
		TimeoutSec: req.TimeoutSec,
		// The wait must finish inside the CLI client's 30s HTTP budget (the job's own
		// deadline is 30s): stop waiting a little earlier and report the still-running
		// job as a failure carrying its id — the job finishes on its own and
		// `gofer job show` tells the truth (never a silent double-injection).
		WaitTimeoutSec: injectWaitSec,
	}, true)
	if err != nil {
		return sessionrelay.InjectResult{}, err
	}
	res := sessionrelay.InjectResult{JobID: out.ID, ExitCode: out.ExitCode}
	if b, lerr := x.jobs.TailLog(out.ID, store.StreamStdout, 4096); lerr == nil {
		res.Output = string(b)
	}
	// A job that never ran (rejected/queued/timeout/cancelled) or one that ran
	// under a non-done status must never read as "the pane got the text". The
	// runner reports -1 for a job it could not start; keep any real exit code.
	if async || out.Status != job.StatusDone {
		if res.ExitCode == 0 {
			res.ExitCode = -1
		}
		if res.Output == "" {
			res.Output = out.Error
		} else if out.Error != "" {
			res.Output = out.Error + "\n" + res.Output
		}
	}
	return res, nil
}

// runnerKeyForSession maps a registered session's runner label to a job runner
// key: the server's own label ("server", or empty on an old registration) means
// its built-in local runner; a configured runner id (a worker's key) passes
// through, and the job service resolves/rejects it like any other submit.
//
// The label vocabulary is shared with the rest of the system (config alias
// helpers), so a session registered as "server" and a job submitted as "local"
// land on the same runner by construction rather than by two copies of a switch.
func runnerKeyForSession(runner string) string {
	if strings.TrimSpace(runner) == "" {
		return config.BuiltinLocalRunner
	}
	return config.NormalizeRunnerName(runner)
}

// PlanTakeover answers what continuing this session would take (design §9.1 B):
// the agent's interactive resume argv, the project's interactive switch, and the
// project root AS THE RUNNER SEES IT — the session's absolute cwd is converted
// against that root, and the pty job resolves its cwd on the runner.
//
// An unknown agent/project is not an error here: it is reported as an empty plan,
// and the relay turns that into the reason code the caller acts on
// (no_resume_template / interactive_not_allowed / cwd_outside_project).
func (x sessionInjector) PlanTakeover(agentKey, projectKey, runner, sessionID string) sessionrelay.TakeoverPlan {
	var plan sessionrelay.TakeoverPlan
	if x.agents == nil || x.projects == nil {
		return plan
	}
	if ac, ok := x.agents.Get(agentKey); ok && ac.Type == agent.TypeCLIAgent && len(ac.SessionResumeInteractive) > 0 {
		plan.Argv = append([]string{ac.Command}, agent.GlobalArgs(ac)...)
		plan.Argv = append(plan.Argv,
			agent.Render(ac.SessionResumeInteractive, agent.Vars{SessionID: sessionID})...)
	}
	cfg := x.projects.Config()
	if cfg == nil {
		return plan
	}
	proj, ok := cfg.Projects[projectKey]
	if !ok {
		return plan
	}
	plan.AllowInteractive = proj.IsInteractiveAllowed()
	// G002: a server-run session's pty starts in THIS process's path view of the
	// project; a worker-run one starts on the worker, which sees the host path.
	if runnerKeyForSession(runner) == runnerLocalKey {
		plan.ExecRoot = cfg.ExecPath(proj)
	} else {
		plan.ExecRoot = proj.HostPath
	}
	return plan
}

// runnerLocalKey is the built-in local runner's key (see runnerKeyForSession).
const runnerLocalKey = config.BuiltinLocalRunner

// TakeoverSession starts path B's interactive job (design §9.1 B): an ordinary
// pty-attached job on the session's own runner carrying the resume argv, primed
// with the reply. It is submitted AS the session's agent (`ResumeSourceAgent`), the
// same authorization a job resume uses — the argv is the agent's own, so this is
// not a new grant of exec — and it returns as soon as the job is accepted, because
// the job lives as long as the human keeps talking.
func (x sessionInjector) TakeoverSession(_ context.Context, req sessionrelay.TakeoverRequest) (sessionrelay.TakeoverResult, error) {
	out, err := x.jobs.Submit(job.JobRequest{
		ProjectKey: req.ProjectKey,
		Agent:      agent.ExecAgentKey,
		Runner:     runnerKeyForSession(req.Runner),
		Cmd:        req.Cmd,
		Cwd:        req.Cwd,
		Title:      req.Title,
		Tags:       req.Tags,
		Cols:       req.Cols,
		Rows:       req.Rows,
		TimeoutSec: req.TimeoutSec,
		// Interactive routes the job to the pty runner and is what makes the web's
		// ?attach=1 terminal work on it.
		Interactive: true,
		// SessionID binds the job to the CLI session it continues; ResumedFrom stays
		// empty because this is an exec carrier, not an acp session/load.
		SessionID:         req.SessionID,
		ResumeSourceAgent: req.ResumeSourceAgent,
		InitialInput:      req.InitialInput,
		CallerID:          req.CallerID,
	})
	if err != nil {
		return sessionrelay.TakeoverResult{}, err
	}
	return sessionrelay.TakeoverResult{JobID: out.ID}, nil
}

// CancelTakeover stops a running takeover job (design §9.1 B release). A job that
// already reached a terminal state is a no-op in the job service, so this only
// fails for a job id the hub has never known — which the release must NOT hide: it
// would leave a live process holding the session the human just took back.
func (x sessionInjector) CancelTakeover(_ context.Context, jobID string) error {
	if err := x.jobs.Cancel(jobID); err != nil && !errors.Is(err, job.ErrJobNotRunning) {
		return err
	}
	return nil
}
