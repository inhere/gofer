package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// jobIDEnv is the variable the job service puts into a job's own process: the id of
// that job. It is the same one gofer's MCP report handler reads (gofer_submit_result),
// so a `gofer job result` typed by an agent inside a job needs no id argument — and the
// credential it authenticates with is that job's already (GOFER_JOB_TOKEN).
const jobIDEnv = "GOFER_JOB_ID"

// jobResultOpts holds `job result` flags. The field names mirror job.SelfResultInput so
// the command stays a thin binder (G021).
var jobResultOpts struct {
	decision       string
	reason         string
	actionRequired string
	findings       gcli.Strings
}

// newJobResultCmd builds `job result [<job-id>]`: the CLI face of gofer_submit_result,
// so an agent whose session has no gofer MCP (suga, codex, an exec wrapper…) can still
// report how its run ended. Same rules as the MCP tool: only the job's own credential
// may write it, and only once.
func newJobResultCmd() *gcli.Command {
	return &gcli.Command{
		Name: "result",
		Desc: "Report how this job ended (pass|rework|fail|human): the CLI face of gofer_submit_result, for agents whose session has no gofer MCP",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.StrOpt(&jobResultOpts.decision, "decision", "d", "", "pass (done as asked) | rework (a follow-up pass is needed) | fail (could not do it) | human (a person has to decide)")
			c.StrOpt(&jobResultOpts.reason, "reason", "r", "", "one-sentence reason for the decision")
			c.StrOpt(&jobResultOpts.actionRequired, "action-required", "", "", "what a person has to decide (with --decision human)")
			c.VarOpt(&jobResultOpts.findings, "finding", "", "an observation worth recording with the report (repeatable)")
			c.AddArg("id", "job id (default: $"+jobIDEnv+", the job this process runs in)", false)
		},
		Func: runJobResult,
	}
}

// runJobResult posts the report to the server and prints the recorded outcome. The
// server's own refusals (another job's credential, a second report, invalid input) come
// back as the client's usual "<status>: <error>: <detail>" text, so they are readable
// without translation here.
func runJobResult(c *gcli.Command, _ []string) error {
	decision := strings.TrimSpace(jobResultOpts.decision)
	if decision == "" {
		return fmt.Errorf("job result requires --decision pass|rework|fail|human")
	}
	if !job.ValidSelfResultDecision(decision) {
		return fmt.Errorf("--decision %q is not one of pass, rework, fail, human", decision)
	}
	reason := strings.TrimSpace(jobResultOpts.reason)
	if reason == "" {
		return fmt.Errorf("job result requires --reason \"…\" (one sentence)")
	}
	id := argID(c)
	if id == "" {
		id = strings.TrimSpace(os.Getenv(jobIDEnv))
	}
	if id == "" {
		return fmt.Errorf("job result needs a <job-id> when %s is unset: only a job reports its own result", jobIDEnv)
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	res, err := cli.SubmitJobResult(id, job.SelfResultInput{
		Decision:       decision,
		Reason:         reason,
		ActionRequired: strings.TrimSpace(jobResultOpts.actionRequired),
		Findings:       trimAll(jobResultOpts.findings),
	})
	if err != nil {
		return err
	}
	printSelfResult(c, res)
	return nil
}

// trimAll trims every entry and drops the empty ones, so a `--finding ""` does not count
// against the report's limits.
func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
