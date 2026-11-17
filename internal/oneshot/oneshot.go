// Package oneshot is the one way gofer asks a model a single, read-only question: a
// one-shot, tool-less, single-turn agent job whose answer is a document the server
// parses. The delivery check is its first user; later decision points use the same
// invoker so every such call has the same guards.
//
// The package sits beside bgagent in the layering: it imports the data layer, bgagent
// (hard stops, run log, audit) and the agent registry, and never the job service. The job
// layer is reached through the JobRunner seam, which the job package implements (the job
// package imports oneshot, never the reverse, G022 / G024).
package oneshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/bgagent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
)

// The kinds of one-shot call. A kind is the unit of the daily limit and of the run log.
const (
	// KindJobCheck is the delivery check (交付核验).
	KindJobCheck = bgagent.KindJobCheck
)

// ErrOutputInvalid is wrapped by the error Run returns when the answer does not satisfy
// Request.OutputSchema.
var ErrOutputInvalid = errors.New("one-shot output does not match the expected schema")

// JobSpec is what the JobRunner is asked to run: one read-only, tool-less job. It carries
// only text and routing; the runner fixes the safety shape.
type JobSpec struct {
	Kind        string
	Project     string
	Agent       string
	Model       string
	Prompt      string
	Args        []string
	Title       string
	SourceJobID string
	Origin      string
	Timeout     time.Duration
}

// JobOutput is the answer of one job.
type JobOutput struct {
	JobID     string
	SessionID string
	Output    []byte
	Usage     *runner.Usage
}

// JobRunner is the seam to the job service: run one read-only job and return its stdout.
type JobRunner interface {
	RunReadOnly(ctx context.Context, spec JobSpec) (JobOutput, error)
}

// Request is one one-shot call.
type Request struct {
	Kind    string
	Project string
	Agent   string
	Model   string
	// Prompt is the full instruction. Input, when set, is appended to it as indented
	// JSON (the structured evidence the model judges).
	Prompt string
	Input  any
	// OutputSchema validates the model's answer; nil accepts any output. A failure is
	// returned wrapping ErrOutputInvalid (the run itself still counts against the limits).
	OutputSchema func(output []byte) error
	// Args must be built with NoToolsArgs (the operator's args followed by the forced
	// tool-less argv); Run refuses a request whose Args do not end with it.
	Args        []string
	Title       string
	SourceJobID string
	Origin      string
	// Limits are the hard stops of the kind (daily runs / cost / tokens, timeout); the
	// counters live in background_agent_runs, so they survive a restart.
	Limits bgagent.Limits
}

// Result is the answer of one call.
type Result struct {
	Output    []byte
	JobID     string
	SessionID string
	Usage     *runner.Usage
}

// Invoker runs one-shot calls.
type Invoker struct {
	bg     *bgagent.Runner
	jobs   JobRunner
	agents *agent.Registry
}

// NewInvoker builds an Invoker over the background-agent runner (guards, run log, audit),
// the job seam and the agent registry (it resolves an agent's no-tools argv).
func NewInvoker(bg *bgagent.Runner, jobs JobRunner, agents *agent.Registry) *Invoker {
	return &Invoker{bg: bg, jobs: jobs, agents: agents}
}

// Run guards the kind's hard stops (a trip is a *bgagent.HardStopError and is audited),
// runs one read-only job, records the run and validates the answer.
func (i *Invoker) Run(ctx context.Context, req Request) (Result, error) {
	if strings.TrimSpace(req.Kind) == "" {
		return Result{}, errors.New("one-shot request needs a kind")
	}
	if strings.TrimSpace(req.Agent) == "" {
		return Result{}, errors.New("one-shot request needs an agent")
	}
	forced, err := forcedArgs(i.agents, req.Agent)
	if err != nil {
		return Result{}, err
	}
	if len(req.Args) < len(forced) || !slices.Equal(req.Args[len(req.Args)-len(forced):], forced) {
		return Result{}, fmt.Errorf("one-shot args for agent %q do not end with the forced no-tools argv", req.Agent)
	}
	prompt := req.Prompt
	if req.Input != nil {
		raw, merr := json.MarshalIndent(req.Input, "", "  ")
		if merr != nil {
			return Result{}, fmt.Errorf("marshal one-shot input: %w", merr)
		}
		prompt += "\n\n" + string(raw)
	}
	res, err := i.bg.Run(ctx, jobExecutor{jobs: i.jobs, req: req}, bgagent.Request{
		Key:    bgagent.Key{Kind: req.Kind, Agent: req.Agent, Project: req.Project, Model: req.Model},
		Prompt: prompt, Title: req.Title, Args: req.Args, Origin: req.Origin, Limits: req.Limits,
	})
	if err != nil {
		return Result{JobID: res.JobID, SessionID: res.SessionID}, err
	}
	out := Result{Output: res.Output, JobID: res.JobID, SessionID: res.SessionID, Usage: res.Usage}
	if req.OutputSchema != nil {
		if verr := req.OutputSchema(res.Output); verr != nil {
			return out, fmt.Errorf("%w: %w", ErrOutputInvalid, verr)
		}
	}
	return out, nil
}

// jobExecutor adapts the JobRunner to bgagent.Executor: a one-shot never continues a
// session, so every run is fresh.
type jobExecutor struct {
	jobs JobRunner
	req  Request
}

func (jobExecutor) Caps(string) bgagent.Caps { return bgagent.Caps{} }

func (e jobExecutor) Execute(ctx context.Context, r bgagent.ExecRequest) (bgagent.ExecResult, error) {
	out, err := e.jobs.RunReadOnly(ctx, JobSpec{
		Kind: e.req.Kind, Project: e.req.Project, Agent: e.req.Agent, Model: e.req.Model, Prompt: r.Prompt,
		Args: r.Args, Title: e.req.Title, SourceJobID: e.req.SourceJobID, Origin: e.req.Origin, Timeout: e.req.Limits.Timeout,
	})
	return bgagent.ExecResult{JobID: out.JobID, SessionID: out.SessionID, Output: out.Output, Usage: out.Usage}, err
}

// NoToolsArgs is the operator's args for the agent followed by the tool-less argv the
// server forces for the agent type. An agent type without a known no-tools argv is
// refused, so a one-shot never runs with tools by omission.
func NoToolsArgs(agents *agent.Registry, agentKey string, operatorArgs []string) ([]string, error) {
	forced, err := forcedArgs(agents, agentKey)
	if err != nil {
		return nil, err
	}
	return append(append([]string(nil), operatorArgs...), forced...), nil
}

// forcedArgs is the forced tool-less argv of an agent (config key first, then the command
// base name).
func forcedArgs(agents *agent.Registry, agentKey string) ([]string, error) {
	names := []string{agentKey}
	if agents != nil {
		if ac, ok := agents.Get(agentKey); ok {
			names = append(names, filepath.Base(strings.ReplaceAll(ac.Command, "\\", "/")))
		}
	}
	forced := config.DeliveryCheckNoToolsArgs(names...)
	if forced == nil {
		return nil, fmt.Errorf("server.delivery_check.agent %q has no known no-tools argv (supported: claude, codex, suga); the checker would run with tools", agentKey)
	}
	return forced, nil
}
