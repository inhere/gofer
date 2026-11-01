package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/memorysynthesis"
	"github.com/inhere/gofer/internal/store"
)

// JobOneShot is the production memorysynthesis.OneShot: one read-only, tool-less, single-turn
// job of a cli-agent on the server's own runner, with the summarizer's internal tag (hidden
// from ordinary lists and excluded from the synthesis materials) and no project rules.
type JobOneShot struct {
	Jobs   *job.Service
	Agents *agent.Registry
}

// noToolsArgs is the argv the server forces after the operator's model_args so the run has
// no tools; an agent without a known no-tools argv is refused (tool use could not be ruled
// out), exactly like the delivery checker.
func (o JobOneShot) noToolsArgs(agentKey string) ([]string, error) {
	names := []string{agentKey}
	if o.Agents != nil {
		if ac, ok := o.Agents.Get(agentKey); ok {
			names = append(names, filepath.Base(strings.ReplaceAll(ac.Command, "\\", "/")))
		}
	}
	forced := config.DeliveryCheckNoToolsArgs(names...)
	if forced == nil {
		return nil, fmt.Errorf("agent %q has no known no-tools argv (supported: claude, codex, suga); the synthesis would run with tools", agentKey)
	}
	return forced, nil
}

// Check says why the agent cannot run a synthesis ("" nil = usable), before any record exists.
func (o JobOneShot) Check(agentKey string) error {
	if o.Agents == nil {
		return errors.New("no agent registry")
	}
	ac, ok := o.Agents.Get(agentKey)
	if !ok {
		return fmt.Errorf("agent %q is not configured", agentKey)
	}
	if ac.Type != agent.TypeCLIAgent {
		return fmt.Errorf("agent %q is not a cli-agent (a one-shot read-only run needs one)", agentKey)
	}
	if av, found := o.Agents.Availability()[agentKey]; found && !av.Available {
		why := av.Error
		if why == "" {
			why = "command not found on this machine"
		}
		return fmt.Errorf("agent %q is unavailable: %s", agentKey, why)
	}
	_, err := o.noToolsArgs(agentKey)
	return err
}

func (o JobOneShot) Run(ctx context.Context, r memorysynthesis.OneShotRequest) (string, error) {
	if o.Jobs == nil {
		return "", errors.New("no job service")
	}
	forced, err := o.noToolsArgs(r.Agent)
	if err != nil {
		return "", err
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = config.DefaultSynthesisTimeout
	}
	res, err := o.Jobs.Submit(job.JobRequest{
		ProjectKey: r.ProjectKey, Agent: r.Agent, Runner: config.BuiltinLocalRunner, Prompt: r.Prompt,
		AgentArgs: append(append([]string(nil), r.Args...), forced...), ReadOnly: true, NoRules: true,
		Title: r.Title, Tags: []string{job.WorkSummarizerJobTag}, TimeoutSec: int(timeout / time.Second),
		EnvDenyExtra: []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT"},
		OriginCaller: r.Origin, InternalExec: job.InternalExecSummarizer,
	})
	if err != nil {
		return "", err
	}
	type waited struct {
		final job.JobResult
		ok    bool
	}
	ch := make(chan waited, 1)
	go func() {
		final, ok := o.Jobs.WaitFor(res.ID, timeout+30*time.Second)
		ch <- waited{final, ok}
	}()
	select {
	case <-ctx.Done():
		_ = o.Jobs.Cancel(res.ID)
		return "", ctx.Err()
	case w := <-ch:
		if !w.ok || !job.IsTerminal(w.final.Status) {
			_ = o.Jobs.Cancel(res.ID)
			return "", errors.New("synthesis job timed out")
		}
		if w.final.Status != job.StatusDone {
			msg := w.final.Error
			if msg == "" {
				msg = w.final.Status
			}
			return "", fmt.Errorf("synthesis job did not succeed (%s)", msg)
		}
		limit := int64(r.MaxOutputBytes) + 1
		if limit <= 1 {
			limit = 64 * 1024
		}
		stdout, _ := o.Jobs.TailLog(res.ID, store.StreamStdout, limit)
		return string(stdout), nil
	}
}
