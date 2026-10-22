package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/work"
	"github.com/inhere/gofer/internal/work/transcript"
	"github.com/inhere/gofer/internal/wsproto"
)

// Wiring of the work-item summarizer's two seams (W2a): the one-shot job runner on top
// of job.Service and the transcript reader (local disk, or the worker's transcript_tail
// frame). They live here, next to the other work adapters, because the work package
// must not import job or the hub (G022).

// workOneShot runs the summarizer as a one-shot, read-only job of a cli-agent. The job
// carries the internal tag so ordinary lists hide it.
type workOneShot struct {
	jobs     *job.Service
	agents   *agent.Registry
	projects *project.Registry
}

// ProjectUsable implements work.ProjectChooser: the project is registered, lists the
// agent (an empty list admits any) and admits the built-in local runner.
func (o workOneShot) ProjectUsable(key, agentKey string) bool {
	if o.projects == nil {
		return false
	}
	p, err := o.projects.Get(key)
	if err != nil {
		return false
	}
	if len(p.AllowedAgents) > 0 && !slices.Contains(p.AllowedAgents, agentKey) {
		return false
	}
	return project.AllowsLocalRunner(p.AllowedRunners)
}

// ProjectDir implements work.ProjectChooser: where a job of that project runs
// (G002: the server's path view).
func (o workOneShot) ProjectDir(key string) string {
	if o.projects == nil {
		return ""
	}
	p, err := o.projects.Get(key)
	if err != nil {
		return ""
	}
	return o.projects.Config().ExecPath(p)
}

// Check says why the agent cannot summarize ("" nil = usable): it must exist as a
// cli-agent (the prompt is an argv, there is no session to keep) and be installed.
func (o workOneShot) Check(agentKey string) error {
	if o.agents == nil {
		return errors.New("没有 agent 注册表")
	}
	ac, ok := o.agents.Get(agentKey)
	if !ok {
		return fmt.Errorf("整理器 agent %q 不存在（在 work.summarizer_agent 里改成已配置的 cli-agent）", agentKey)
	}
	if ac.Type != agent.TypeCLIAgent {
		return fmt.Errorf("整理器 agent %q 不是 cli-agent（一次性只读整理需要 cli-agent）", agentKey)
	}
	if len(ac.ReadOnlyArgs) == 0 {
		return fmt.Errorf("整理器 agent %q 没有只读模式（给它配 agents.%s.read_only_args；claude / codex 已内置）", agentKey, agentKey)
	}
	if av, found := o.agents.Availability()[agentKey]; found && !av.Available {
		why := av.Error
		if why == "" {
			why = "本机没有找到该命令"
		}
		return fmt.Errorf("整理器 agent %q 不可用：%s", agentKey, why)
	}
	return nil
}

// summarizerJobRequest is the one-shot tidy-up job: read-only on the local runner, and
// without rules — the agent's or project's rules are working discipline for real tasks
// and would only cost tokens and pull the model away from the fixed JSON answer.
func summarizerJobRequest(r work.OneShotRequest) job.JobRequest {
	return job.JobRequest{
		ProjectKey: r.ProjectKey, Agent: r.Agent, Runner: config.BuiltinLocalRunner, Prompt: r.Prompt,
		AgentArgs: r.Args, ReadOnly: true, NoRules: true, Title: r.Title, Tags: []string{job.WorkSummarizerJobTag},
		TimeoutSec: r.TimeoutSec, EnvDenyExtra: []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT"},
		// Bounded by the ceiling of the caller that asked for the tidy-up (empty for the
		// timer); the agent argv comes from the work configuration, not from the caller.
		OriginCaller: r.Origin, InternalExec: job.InternalExecSummarizer,
	}
}

// CanContinueSession implements work.SessionKeeper: whether a finished summarizer job of
// this agent can be continued (its config has a session_resume template). Only then does a
// project keep one summarizer session instead of opening one per tidy-up (gofer-ayu0).
func (o workOneShot) CanContinueSession(agentKey string) bool {
	if o.agents == nil {
		return false
	}
	ac, ok := o.agents.Get(agentKey)
	if !ok {
		return false
	}
	return len(ac.SessionResume) > 0
}

func (o workOneShot) Run(ctx context.Context, r work.OneShotRequest) (work.OneShotResult, error) {
	if strings.TrimSpace(r.ProjectKey) == "" {
		return work.OneShotResult{}, errors.New("整理 job 没有可用的项目：在 work.summarizer_project 里指定一个项目，或在配置里保留 default 项目")
	}
	res, err := o.jobs.Submit(summarizerJobRequest(r))
	if err != nil {
		return work.OneShotResult{}, err
	}
	return o.finish(ctx, res.ID, r.TimeoutSec)
}

// Resume continues the session of r.JobID. The continuation is a normal `job resume`:
// it inherits the source's read-only / tag / timeout, so the tidy-up stays hidden and
// bounded like the run it continues.
func (o workOneShot) Resume(ctx context.Context, r work.ResumeRequest) (work.OneShotResult, error) {
	if strings.TrimSpace(r.ProjectKey) == "" {
		return work.OneShotResult{}, errors.New("整理 job 没有可用的项目：在 work.summarizer_project 里指定一个项目，或在配置里保留 default 项目")
	}
	res, err := o.jobs.ResumeJob(r.JobID, r.Prompt, "", "")
	if err != nil {
		return work.OneShotResult{JobID: res.ID}, err
	}
	return o.finish(ctx, res.ID, r.TimeoutSec)
}

// finish waits for a submitted summarizer job and reads its output: both the fresh run and
// the continuation end here.
func (o workOneShot) finish(ctx context.Context, jobID string, timeoutSec int) (work.OneShotResult, error) {
	out := work.OneShotResult{JobID: jobID}
	wait := time.Duration(timeoutSec+30) * time.Second
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < wait {
		wait = time.Until(dl)
	}
	final, ok := o.jobs.WaitFor(jobID, wait)
	if !ok || !job.IsTerminal(final.Status) {
		_ = o.jobs.Cancel(jobID)
		return out, errors.New("整理 job 超时")
	}
	if final.Status != job.StatusDone {
		msg := final.Error
		if msg == "" {
			msg = final.Status
		}
		return out, fmt.Errorf("整理 job 未成功（%s）", msg)
	}
	stdout, _ := o.jobs.TailLog(jobID, store.StreamStdout, 64*1024)
	out.Output = string(stdout)
	out.SessionID = final.SessionID
	return out, nil
}

// workTranscripts reads a session's registered transcript: straight off the disk when
// the session ran on the server's own machine, through the worker's transcript_tail
// frame otherwise. Anything it cannot read is work.ErrNoTranscript so the summarizer
// degrades instead of failing.
type workTranscripts struct{ s *Server }

// transcriptTailHub is the optional hub capability (kept off workerHub so test fakes
// need not implement it).
type transcriptTailHub interface {
	SendTranscriptTail(ctx context.Context, workerID string, req wsproto.TranscriptTail) (wsproto.TranscriptTailResult, error)
}

func newTailReqID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "tt-" + hex.EncodeToString(b[:])
}

// ConfiguredDialect implements work.DialectSource from the live agent config.
func (t workTranscripts) ConfiguredDialect(agentKey string) string {
	if t.s.agents == nil {
		return ""
	}
	if ac, ok := t.s.agents.Get(agentKey); ok {
		return ac.TranscriptDialect
	}
	return ""
}

func (t workTranscripts) ReadTail(ctx context.Context, a jobstore.AgentSession, maxBytes int64) ([]byte, error) {
	path := strings.TrimSpace(a.Transcript)
	if path == "" {
		return nil, fmt.Errorf("%w: the session registered no transcript", work.ErrNoTranscript)
	}
	rk := runnerKeyForSession(a.Runner)
	if rk == config.BuiltinLocalRunner {
		data, _, _, err := transcript.ReadTailSafe(path, maxBytes)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", work.ErrNoTranscript, err)
		}
		return data, nil
	}
	workerID := ""
	if t.s.projects != nil {
		if cfg := t.s.projects.Config(); cfg != nil {
			workerID = cfg.Runners[rk].WorkerID
		}
	}
	h, ok := t.s.hub.(transcriptTailHub)
	if workerID == "" || !ok {
		return nil, fmt.Errorf("%w: runner %q has no worker to read it from", work.ErrNoTranscript, rk)
	}
	res, err := h.SendTranscriptTail(ctx, workerID, wsproto.TranscriptTail{
		ReqID: newTailReqID(), SessionID: a.SessionID, Path: path, MaxBytes: maxBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", work.ErrNoTranscript, err)
	}
	if !res.OK {
		return nil, fmt.Errorf("%w: %s", work.ErrNoTranscript, res.Error)
	}
	return res.Data, nil
}
