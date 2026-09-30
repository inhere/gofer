package job

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/util"
)

// appendSessionStore preserves earlier turns when a serve rebuilds a local ACP
// session job. The normal FileStore.LogWriter truncates for a new job.
type appendSessionStore struct{ *store.FileStore }

func (s appendSessionStore) LogWriter(id string, stream store.Stream) (io.WriteCloser, error) {
	w, _, err := s.AppendLogWriter(id, stream)
	return w, err
}

func (s *Service) failLocalSessionRecovery(id string, cause error) {
	reason := fmt.Sprintf("ACP session/load recovery failed: %v; start a new session", cause)
	if n, err := s.meta.FailRecoveringJob(id, s.nowFn().Unix(), reason); err == nil && n > 0 {
		s.recordEvent(id, EventJobTerminal, map[string]any{"status": StatusFailed, "error": reason})
	}
}

func (s *Service) resumeLocalSession(rec jobstore.JobRecord) error {
	if rec.SessionID == "" {
		return fmt.Errorf("session id missing")
	}
	var request JobRequest
	if err := json.Unmarshal([]byte(rec.RequestJSON), &request); err != nil {
		return fmt.Errorf("stored request: %w", err)
	}
	cfg := s.config()
	proj, ok := cfg.Projects[rec.ProjectKey]
	if !ok {
		return fmt.Errorf("project %q is no longer configured", rec.ProjectKey)
	}
	ac, ok := agent.ResolveAgent(cfg, rec.Agent)
	if !ok || ac.Type != agent.TypeACPAgent {
		return fmt.Errorf("agent %q is no longer an acp-agent", rec.Agent)
	}
	if ac.ACP != nil && ac.ACP.LoadSession != nil && !*ac.ACP.LoadSession {
		return fmt.Errorf("agent %q does not support session/load", rec.Agent)
	}
	run := s.runners[builtinACPRunner]
	if run == nil {
		return fmt.Errorf("ACP runner unavailable")
	}
	resolved, err := agent.BuildFrom(cfg, rec.Agent, "", request.Cmd, agent.Vars{
		Cwd: rec.Cwd, JobID: rec.ID, ResultDir: rec.ResultDir,
	}, agent.BuildOptions{AllowEmptyPrompt: true, AgentArgs: request.AgentArgs, ReadOnly: rec.ReadOnly})
	if err != nil {
		return fmt.Errorf("rebuild ACP command: %w", err)
	}
	secretMap, err := LoadEnvFilesMap(request.EnvFiles, cfg, proj)
	if err != nil {
		return fmt.Errorf("reload env files: %w", err)
	}
	fs := store.NewFileStore(filepath.Dir(rec.ResultDir))
	entry := &jobEntry{
		result:          fromRecord(rec),
		store:           appendSessionStore{fs},
		done:            make(chan struct{}),
		sessionCommands: make(chan runner.SessionCommand, 1),
	}
	runReq := runner.Request{
		JobID: rec.ID, WorkDir: rec.Cwd,
		Command: resolved.Command, Args: resolved.Args,
		Env:     goferJobEnv(util.MergeEnv(util.MergeEnv(secretMap, resolved.Env), request.Env), rec.ID, rec.Cwd, rec.ResultDir),
		EnvDeny: effectiveJobEnvDeny(cfg), EnvAllow: proj.JobEnvAllow,
		Approvals: approvalSink{s: s, jobID: rec.ID},
	}
	runReq.Env = util.EnvWith(runReq.Env, s.jobCredentialEnv(cfg, rec.ID, request, sessionCredentialTTL(request, time.Duration(rec.TimeoutSec)*time.Second)))
	for _, rel := range request.LockPaths {
		path, pathErr := project.SafeJoin(cfg.ExecPath(proj), rel)
		if pathErr != nil {
			return fmt.Errorf("rebuild lock path %q: %w", rel, pathErr)
		}
		runReq.LockPaths = append(runReq.LockPaths, path)
	}
	runReq.ACP = acpRequest(cfg, ac, request, "", rec.ResultDir)
	runReq.ACP.LoadSessionID = rec.SessionID
	runReq.ACP.AppendEvents = true
	s.configureResidentACP(entry, runReq.ACP, rec.TimeoutSec)
	// execute installs the output writers and uses this callback for permission
	// events exactly as it does for a freshly submitted job.
	s.mu.Lock()
	if s.jobs[rec.ID] != nil {
		s.mu.Unlock()
		return fmt.Errorf("job %s already active", rec.ID)
	}
	s.jobs[rec.ID] = entry
	s.mu.Unlock()
	stall := 0
	if request.StallTimeoutSec != nil {
		stall = *request.StallTimeoutSec
	}
	dirWait := cfg.EffectiveDirLockMaxWaitSec(request.DirWaitMaxSec)
	go s.execute(entry, run, execGates{
		project:   s.semaphore(rec.ProjectKey, proj.MaxConcurrentJobs),
		caller:    s.callerSemaphore(rec.CallerID, cfg.Server.CallerConcurrencyLimit(rec.CallerID)),
		agent:     s.agentSemaphore(rec.Agent, ac.MaxConcurrent),
		exclusive: rec.DirExclusive,
		stall:     stall,
		dirWait:   dirWait,
	}, runReq, time.Duration(rec.TimeoutSec)*time.Second)
	return nil
}
