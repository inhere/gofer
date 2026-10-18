package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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
	// workerProjectDir returns where a worker-run project actually lives on that
	// worker (its heartbeat report, POLICY roots applied), "" when unknown.
	workerProjectDir func(runner, projectKey string) string
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
		ProjectKey: projectKey, Agent: agent.ExecAgentKey, Runner: x.runnerKey(runner),
		Cmd: command, Cwd: ".", Title: title, Tags: []string{job.MessengerJobTag},
		Env: map[string]string{"GOFER_MESSENGER": "1"},
		// gofer-qzv5: a web message handed to the one-shot messenger is the same
		// control-plane delivery as path A/C — an update waiting for idle must not park
		// it (see InjectRequest.UpdateExempt).
		UpdateExempt: true,
		MessengerMeta: &job.MessengerMeta{
			TargetSession: strings.TrimSpace(strings.TrimPrefix(title, "session messenger · ")),
			Message:       messengerOriginalMessage(command),
			Channel:       messengerJobChannel(x.runnerKey(runner)),
		},
		TimeoutSec: int(timeout / time.Second), CallerID: "", EnvDenyExtra: []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT"},
		// The message belongs to the caller that sent it (bounded by that caller's ceiling);
		// the messenger command itself is gofer's own.
		OriginCaller: caller, InternalExec: job.InternalExecRelay,
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

// messengerJobChannel takes the CANONICAL runner key (see runnerKeyForSession).
func messengerJobChannel(runnerKey string) string {
	if runnerKey == config.BuiltinLocalRunner {
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
	return true, out.Status, out.ExitCode, x.jobOutput(jobID, out), nil
}

// AbandonMessengerJob settles a one-shot messenger job gofer stops waiting for
// (sessionrelay.MessengerAbandoner, gofer-kx53) exactly like a path A/C delivery that
// outlived its wait window: a job that never began is cancelled, a running one is kept.
func (x sessionInjector) AbandonMessengerJob(jobID string) (sessionrelay.InjectResult, error) {
	return x.gaveUp(job.JobResult{ID: jobID}), nil
}

// InjectJob is the relay's probe into a delivery job that outlived its wait window
// (sessionrelay.InjectProbe, gofer-qzv5): the message is 「送达中」 until this says
// otherwise. Done is "the process is over" rather than "terminal", so a delivery job
// waiting for review does not keep a message 「送达中」 for ever.
//
// An unknown job id is reported as unknown (Known=false) instead of an error: after a
// server restart the job record may be gone, and the relay then reports the delivery as
// unresolved rather than leaving it pending.
func (x sessionInjector) InjectJob(jobID string) (sessionrelay.InjectState, error) {
	out, ok := x.jobs.Get(jobID)
	if !ok {
		return sessionrelay.InjectState{}, nil
	}
	if !job.IsFinished(out.Status) {
		return sessionrelay.InjectState{Known: true, Status: out.Status}, nil
	}
	return sessionrelay.InjectState{Known: true, Done: true, Status: out.Status,
		ExitCode: out.ExitCode, Output: x.jobOutput(jobID, out)}, nil
}

// jobOutput is the bounded stdout+stderr tail of a finished job, falling back to the
// job's own error text when it printed nothing (the shape the messenger bridge and the
// delivery write-back both report).
func (x sessionInjector) jobOutput(jobID string, out job.JobResult) string {
	stdout, _ := x.jobs.TailLog(jobID, store.StreamStdout, 4096)
	stderr, _ := x.jobs.TailLog(jobID, store.StreamStderr, 4096)
	output := strings.TrimSpace(string(stdout))
	if s := strings.TrimSpace(string(stderr)); s != "" {
		if output != "" {
			output += "\n"
		}
		output += s
	}
	if output == "" {
		output = out.Error
	}
	return output
}

// InjectSession runs one injection job to completion and reports its exit code
// plus the stdout tail (the pane check writes its reason code there).
func (x sessionInjector) InjectSession(_ context.Context, req sessionrelay.InjectRequest) (sessionrelay.InjectResult, error) {
	out, async, err := x.jobs.SubmitSync(job.JobRequest{
		ProjectKey: req.ProjectKey,
		Agent:      agent.ExecAgentKey,
		Runner:     x.runnerKey(req.Runner),
		Cmd:        req.Cmd,
		Cwd:        req.Cwd,
		Title:      req.Title,
		Tags:       req.Tags,
		TimeoutSec: req.TimeoutSec,
		// Path C: the agent's own deliver_command (carrier admitted as that agent).
		Stdin:             req.Stdin,
		ResumeSourceAgent: req.SourceAgent,
		// gofer-dzv3 / gofer-qzv5: a delivery is gofer's own short control-plane work,
		// so an update waiting for the machine to go idle must not park it — parking a
		// delivery loses the message it carries. The relay owns the flag; this adapter
		// only carries it onto the job request.
		UpdateExempt: req.UpdateExempt,
		// The wait must finish inside the CLI client's 30s HTTP budget (the job's own
		// deadline is 30s): stop waiting a little earlier and settle the still-pending
		// job the honest way — see gaveUp (never a silent double-injection, and no
		// false 「失败」 for a job that is still on its way).
		WaitTimeoutSec: injectWaitSec,
		// The injection belongs to the caller that caused the message; its script / the
		// agent's deliver_command is gofer's own composition.
		OriginCaller: req.OriginCaller, InternalExec: job.InternalExecRelay,
	}, true)
	if err != nil {
		return sessionrelay.InjectResult{}, err
	}
	if async {
		return x.gaveUp(out), nil
	}
	res := x.injectResult(out)
	// A job that never ran (rejected/queued/timeout/cancelled) or one that ran
	// under a non-done status must never read as "the pane got the text". The
	// runner reports -1 for a job it could not start; keep any real exit code.
	if out.Status != job.StatusDone {
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

// injectResult reads one finished (or failed) injection job's exit code and output tail.
func (x sessionInjector) injectResult(out job.JobResult) sessionrelay.InjectResult {
	return sessionrelay.InjectResult{JobID: out.ID, ExitCode: out.ExitCode, Output: x.jobOutput(out.ID, out)}
}

// gaveUp resolves what the expiry of the wait window means for the TEXT (gofer-qzv5).
// SubmitSync's snapshot is the SUBMIT-time one, so the job is re-read here:
//
//   - it ended meanwhile  → its real result (exit code and output);
//   - it is running       → Running: the text may still land, so the caller records
//     「送达中」 and the terminal write-back says how it ended (InjectJob);
//   - it had not started  → CANCELLED here, so nothing can arrive late, and reported
//     with the status it sat in (a job parked by an update waiting for idle is the case
//     that motivated this: it used to be reported as exit code -1, 「失败」).
//
// Only the third case promises "nothing was delivered", which is what makes a retry
// safe, so it is also the case that must be airtight: a withdraw that fails — or a job
// that began between the read and the cancel — is reported as Running instead.
func (x sessionInjector) gaveUp(initial job.JobResult) sessionrelay.InjectResult {
	cur, ok := x.jobs.Get(initial.ID)
	if !ok {
		return sessionrelay.InjectResult{JobID: initial.ID, ExitCode: -1, Status: "unknown",
			StatusDetail: "job 记录已不存在"}
	}
	if job.IsFinished(cur.Status) {
		res := x.injectResult(cur)
		if cur.Status != job.StatusDone {
			if res.ExitCode == 0 {
				res.ExitCode = -1
			}
			if res.Output == "" {
				res.Output = cur.Error
			}
		}
		return res
	}
	if !job.Unstarted(cur.Status) {
		return sessionrelay.InjectResult{JobID: cur.ID, ExitCode: -1, Running: true, Status: cur.Status}
	}
	// Never started: withdraw it (a queued job honours the intent the moment it would
	// start; a parked / held one is a compare-and-set).
	if cerr := x.jobs.Cancel(cur.ID); cerr != nil {
		slog.Warn("session relay: could not withdraw an unstarted delivery job",
			"job_id", cur.ID, "status", cur.Status, "err", cerr)
		return sessionrelay.InjectResult{JobID: cur.ID, ExitCode: -1, Running: true, Status: cur.Status}
	}
	// It may still have started between the read and the cancel, in which case the withdraw
	// killed a delivery that could already have handed the text over: the job's own
	// lifecycle says which happened (see job.Service.Ran), and only a job that never ran
	// lets the caller promise that nothing was delivered — which is what a retry needs.
	if ran, rerr := x.jobs.Ran(cur.ID); rerr != nil {
		slog.Warn("session relay: could not tell whether a delivery job ran",
			"job_id", cur.ID, "err", rerr)
		return sessionrelay.InjectResult{JobID: cur.ID, ExitCode: -1, Running: true, Status: cur.Status}
	} else if ran {
		return sessionrelay.InjectResult{JobID: cur.ID, ExitCode: -1, Running: true, Status: cur.Status}
	}
	return sessionrelay.InjectResult{JobID: cur.ID, ExitCode: -1, Status: cur.Status,
		StatusDetail: unstartedDetail(cur)}
}

// unstartedDetail explains a job that never started in one line for the message's
// 「未送达」 reason: its status in the human's language, plus the job's own error text when
// it has one. The status carries the answer a reader needs — a delivery job parked by an
// update waiting for idle reads 「升级等待（新任务暂停启动）」，not an exit code nobody
// produced.
func unstartedDetail(out job.JobResult) string {
	detail := job.StatusLabelZH(out.Status)
	if reason := strings.TrimSpace(out.Error); reason != "" {
		return detail + "：" + reason
	}
	return detail
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

// runnerKey is runnerKeyForSession as a method of the injector.
func (x sessionInjector) runnerKey(runner string) string {
	return runnerKeyForSession(runner)
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
		// The server can look at its own filesystem: the wake-up directory is
		// chosen among directories that really exist (M8).
		plan.DirExists = localDirExists
	} else {
		plan.ExecRoot = proj.HostPath
		// A POLICY worker maps the host path through its roots (a container sees
		// /d/work/... for D:/work/...); judging the session cwd against the raw host
		// path then calls every in-project cwd "outside the project".
		if x.workerProjectDir != nil {
			if dir := x.workerProjectDir(runner, projectKey); dir != "" {
				plan.ExecRoot = dir
			}
		}
	}
	// The project root's other path views: a session's cwd/transcript may have been
	// recorded in a view other than the one the runner executes in.
	for _, alt := range []string{proj.HostPath, proj.ContainerPath} {
		if alt != "" && alt != plan.ExecRoot && !slices.Contains(plan.AltRoots, alt) {
			plan.AltRoots = append(plan.AltRoots, alt)
		}
	}
	return plan
}

// localDirExists asks THIS machine whether path is a directory.
func localDirExists(path string) (exists, known bool) {
	st, err := os.Stat(filepath.FromSlash(path))
	return err == nil && st.IsDir(), true
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
		Runner:     x.runnerKey(req.Runner),
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
		OriginCaller:      req.OriginCaller,
		InternalExec:      job.InternalExecRelay,
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
