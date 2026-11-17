package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/store"
)

// maxSessionRedispatch bounds how many times one execute reloads a session onto a restarted
// worker process; every attempt needs the worker to restart again, so the cap is a backstop
// against a loop, not a limit a healthy session meets.
const maxSessionRedispatch = 8

// EventSessionTurnInterrupted is recorded when a worker restart cut a turn short:
// {turn_no, reason}. The session itself is reloaded and waits for the next message.
const EventSessionTurnInterrupted = "session.turn_interrupted"

// sessionNoteTurnInterrupted is the transcript note code the timeline renders as "the last
// turn was interrupted because the worker restarted".
const sessionNoteTurnInterrupted = "turn_interrupted_worker_restart"

// sessionRedispatchable reports whether a worker session whose process restarted can be
// reloaded on the new process: a persistent session with a known agent session id, an agent
// that supports session/load (the same test `job resume` makes), no managed worktree or
// temporary checkout (a re-dispatch would create another one), and a recovery window to wait
// for the new process in.
func sessionRedispatchable(cfg *config.Config, agentKey string, sessionID string, f *runner.Forward) bool {
	if f == nil || !f.Session || sessionID == "" || f.Worktree || f.Ref != "" || f.Interactive || f.Messenger != nil {
		return false
	}
	if cfg.JobRecoverWindow() <= 0 {
		return false
	}
	// An agent only the worker knows is not the hub's to judge: the worker's own agent
	// decides, and a session it cannot load ends with that explanation.
	ac, ok := agent.ResolveAgent(cfg, agentKey)
	return !ok || (ac.Type == agent.TypeACPAgent && ac.ACP.AllowsLoadSession())
}

// redispatchForward is the forward of a session reloaded after its worker process restarted:
// the original dispatch minus everything that only belongs to the first one (the prompt, the
// staged uploads, collect, skills and verify), plus the session to load and a bounded wait
// for the new process to register.
func redispatchForward(orig runner.Forward, jobID, sessionID string, wait time.Duration) *runner.Forward {
	f := orig
	f.Prompt = ""
	f.SessionID = sessionID
	f.ResumedFrom = jobID
	f.AutoResumeAttempt = 0
	f.Uploads, f.Collect, f.Skills, f.Verify, f.InitialInput = nil, nil, nil, nil, ""
	f.WaitWorkerOnline = wait
	return &f
}

// redispatchWorkerSession keeps reloading a session onto a restarted worker process for as
// long as the run ends in a worker restart: execute calls it with the first run's result and
// gets back the result of the last. Anything else, a job that cannot be reloaded or one that
// was cancelled or is being ended, comes back untouched and fails as it always did.
func (s *Service) redispatchWorkerSession(ctx context.Context, entry *jobEntry, run runner.Runner, req *runner.Request, res runner.Result) runner.Result {
	for n := 0; n < maxSessionRedispatch && errors.Is(res.Err, runner.ErrWorkerRestarted) && ctx.Err() == nil; n++ {
		if !s.prepareSessionRedispatch(entry, req) {
			break
		}
		res = run.Run(ctx, *req)
	}
	return res
}

// prepareSessionRedispatch turns req into the re-dispatch of entry's session and moves the
// job to `recovering` while it waits for the new worker process. false = not reloadable.
func (s *Service) prepareSessionRedispatch(entry *jobEntry, req *runner.Request) bool {
	entry.mu.Lock()
	snap := entry.result
	stop := entry.shutdownRequested || entry.manualEndRequested || entry.sessionEnding
	entry.mu.Unlock()
	cfg := s.config()
	if stop || !sessionRedispatchable(cfg, snap.Agent, snap.SessionID, req.Forward) {
		return false
	}
	s.reattachSession(entry, snap)
	req.Forward = redispatchForward(*req.Forward, snap.ID, snap.SessionID, cfg.JobRecoverWindow())
	// The worker's started report is what proves the reloaded session is back; until it
	// arrives the job is `recovering`, and then the session status takes over.
	started, startedAt := req.OnStarted, req.OnStartedAt
	req.OnStarted = func() {
		s.clearRecovering(entry, snap.ID)
		if started != nil {
			started()
		}
	}
	req.OnStartedAt = func(t int64) {
		s.clearRecovering(entry, snap.ID)
		if startedAt != nil {
			startedAt(t)
		}
	}
	return true
}

// reattachSession does the bookkeeping the host owes a session whose worker process is being
// replaced: the turn counter the new process restarts from zero is shifted, a turn cut short
// is recorded, the questions the dead process asked are closed, and the job waits as
// `recovering`.
func (s *Service) reattachSession(entry *jobEntry, snap JobResult) {
	entry.mu.Lock()
	entry.turnBase = snap.TurnNo
	var pending []string
	for _, rec := range entry.interactions {
		if rec.data.Status == InteractionPending {
			pending = append(pending, rec.data.ID)
		}
	}
	entry.mu.Unlock()
	if snap.Status == StatusRunning || snap.Status == StatusPendingInteraction {
		s.recordEvent(snap.ID, EventSessionTurnInterrupted, map[string]any{"turn_no": snap.TurnNo, "reason": "worker_restarted"})
		appendSessionNote(snap.ResultDir, sessionNoteTurnInterrupted)
	}
	for _, id := range pending {
		if err := s.cancelInteraction(snap.ID, id); err != nil {
			slog.Warn("close interaction of a restarted worker", "job_id", snap.ID, "interaction_id", id, "err", err)
		}
	}
	if len(pending) > 0 {
		appendSessionNote(snap.ResultDir, staleInteractionReason)
	}
	s.setRecovering(entry, snap.ID, "worker process restarted; reloading the session")
	s.recordEvent(snap.ID, EventJobDispatched, map[string]any{"runner": snap.Runner, "worker_id": snap.WorkerID, "reason": "worker_restarted"})
}

// resumeRemoteSession reloads a worker session whose job has no execute goroutine behind
// it: the serve that dispatched it restarted and the worker process restarted too, so the
// stored row is all that is left. It runs the job again under the SAME id on the new worker
// process (existing is the adopted entry to reuse, nil to build one from the row). The caller
// has already established that the session may be reloaded.
func (s *Service) resumeRemoteSession(rec jobstore.JobRecord, existing *jobEntry) error {
	var request JobRequest
	if err := json.Unmarshal([]byte(rec.RequestJSON), &request); err != nil {
		return fmt.Errorf("stored request: %w", err)
	}
	cfg := s.config()
	proj, ok := cfg.Projects[rec.ProjectKey]
	if !ok {
		return fmt.Errorf("project %q is no longer configured", rec.ProjectKey)
	}
	run := s.runner(rec.Runner)
	if run == nil || !isWorkerRunner(cfg, rec.Runner) {
		return fmt.Errorf("runner %q is not available", rec.Runner)
	}
	timeout := time.Duration(rec.TimeoutSec) * time.Second
	fwd := remoteForward(request, "", rec.TimeoutSec, runner.Request{})
	fwd.WorkerID = rec.WorkerID
	if !sessionRedispatchable(cfg, rec.Agent, rec.SessionID, fwd) {
		return fmt.Errorf("session of agent %q cannot be reloaded", rec.Agent)
	}
	kind, planID := jobCredentialIdentity(request)
	if tok, err := s.issueJobToken(rec.ID, kind, planID, timeout); err != nil {
		slog.Warn("job credential: issue for re-dispatch", "job_id", rec.ID, "err", err)
	} else {
		fwd.JobToken = tok
	}
	fwd = redispatchForward(*fwd, rec.ID, rec.SessionID, cfg.JobRecoverWindow())
	req := runner.Request{
		JobID: rec.ID, WorkDir: rec.Cwd, ResultDir: rec.ResultDir, Forward: fwd,
		Interactions: remoteInteractionSink{s: s, jobID: rec.ID},
	}
	var secretValues []string
	if len(request.Secrets) > 0 {
		_, values, err := resolveSecrets(cfg, request.Secrets)
		if err != nil {
			return fmt.Errorf("reload secrets: %w", err)
		}
		secretValues = values
	}
	entry := existing
	s.mu.Lock()
	if entry == nil {
		if s.jobs[rec.ID] != nil {
			s.mu.Unlock()
			return fmt.Errorf("job %s already active", rec.ID)
		}
		entry = &jobEntry{
			result: fromRecord(rec),
			store:  appendSessionStore{store.NewFileStore(filepath.Dir(rec.ResultDir))},
			done:   make(chan struct{}),
		}
		s.jobs[rec.ID] = entry
	} else {
		entry.store = appendSessionStore{store.NewFileStore(filepath.Dir(rec.ResultDir))}
	}
	entry.secretValues = secretValues
	s.mu.Unlock()
	s.reattachSession(entry, entry.snapshot())
	stall := 0
	if request.StallTimeoutSec != nil {
		stall = *request.StallTimeoutSec
	}
	go s.execute(entry, run, execGates{
		project: s.semaphore(rec.ProjectKey, proj.MaxConcurrentJobs),
		caller:  s.callerSemaphore(rec.CallerID, cfg.Server.CallerConcurrencyLimit(rec.CallerID)),
		agent:   s.agentSemaphore(rec.Agent, agentMaxConcurrent(cfg, rec.Agent)),
		stall:   stall,
	}, req, timeout)
	return nil
}

func agentMaxConcurrent(cfg *config.Config, key string) int {
	if ac, ok := agent.ResolveAgent(cfg, key); ok {
		return ac.MaxConcurrent
	}
	return 0
}

// reloadSession hands an adopted job whose worker process restarted over to a re-dispatch of
// its session: the adopted handle stops driving the job (its log writers close, the host
// context goes quiet) and an execute goroutine takes the same entry over. false = the job
// is not a session that can be reloaded, and the caller finishes it as lost.
func (a *AdoptedJob) reloadSession() bool {
	rec, ok, err := a.s.meta.GetJob(a.jobID)
	if err != nil || !ok || a.delivered.Load() {
		return false
	}
	snap := a.entry.snapshot()
	var request JobRequest
	if json.Unmarshal([]byte(rec.RequestJSON), &request) != nil || !request.Session {
		return false
	}
	cfg := a.s.config()
	if !sessionRedispatchable(cfg, rec.Agent, snap.SessionID, remoteForward(request, "", rec.TimeoutSec, runner.Request{})) {
		return false
	}
	if !a.delivered.CompareAndSwap(false, true) {
		return true
	}
	close(a.done)
	_ = a.stdout.Close()
	_ = a.stderr.Close()
	a.cancel()
	a.entry.mu.Lock()
	a.entry.adopted = nil
	a.entry.mu.Unlock()
	rec.SessionID = snap.SessionID
	if err := a.s.resumeRemoteSession(rec, a.entry); err != nil {
		// The handle is already given up, so the job cannot be left half-adopted: end it
		// as the restart it was.
		slog.Warn("reload worker session", "job_id", a.jobID, "err", err)
		runErr := remoteSessionWorkerRestartError(a.s.notifyLang(snap.ProjectKey), a.jobID)
		a.s.finish(a.entry, a.jobID, StatusFailed, -1, runErr)
		close(a.entry.done)
	}
	return true
}
