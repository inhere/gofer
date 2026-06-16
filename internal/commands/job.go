package commands

import (
	"fmt"
	"os"
	"time"

	"github.com/gookit/gcli/v3"

	"dev-agent-bridge/internal/client"
	"dev-agent-bridge/internal/config"
	"dev-agent-bridge/internal/job"
)

// rawCmd holds the tokens after a `--` separator on the command line. It is set
// once by the process entry point (main, via SetRawCmd) before gcli parses the
// remaining args, and read by `job run` to fill JobRequest.Cmd. Tests set it
// directly. This package-level stash is the bridge between SplitRawArgs (which
// strips the tail so gcli never sees it) and the job run handler.
var rawCmd []string

// SetRawCmd records the raw command tail (the tokens after `--`). main calls it
// with the result of SplitRawArgs; tests may call it to simulate `-- raw cmd`.
func SetRawCmd(cmd []string) { rawCmd = cmd }

// jobRunOpts holds `job run` flags. prompt may also arrive as the positional
// <prompt> argument (see §8.2); the flag is the alternative spelling.
var jobRunOpts = struct {
	config  string
	server  string
	token   string
	project string
	agent   string
	runner  string
	cwd     string
	prompt  string
	timeout int
	title   string
	wait    bool
}{}

// jobCommonOpts holds the --config/--server/--token flags shared by
// show/logs/cancel.
var jobCommonOpts = struct {
	showConfig, showServer, showToken     string
	logsConfig, logsServer, logsToken     string
	logsStream                            string
	cancelConfig, cancelServer, cancelTkn string
}{}

// NewJobCmd builds the `job` command group (run/show/logs/cancel). It wraps the
// server's /v1/jobs HTTP API so the host can drive jobs without curl (plan §9-P6).
func NewJobCmd() *gcli.Command {
	return &gcli.Command{
		Name: "job",
		Desc: "Submit and manage jobs via the bridge server",
		Subs: []*gcli.Command{
			{
				Name: "run",
				Desc: "Submit a new job",
				Config: func(c *gcli.Command) {
					c.StrOpt(&jobRunOpts.config, "config", "c", "", "path to the bridge config file")
					c.StrOpt(&jobRunOpts.server, "server", "s", "", "server address (overrides config server.addr)")
					c.StrOpt(&jobRunOpts.token, "token", "", "", "bearer token override (prefer config/env)")
					c.StrOpt(&jobRunOpts.project, "project", "p", "", "project key (required)")
					c.StrOpt(&jobRunOpts.agent, "agent", "a", "", "agent key (required)")
					c.StrOpt(&jobRunOpts.runner, "runner", "", "local", "runner key")
					c.StrOpt(&jobRunOpts.cwd, "cwd", "", ".", "working dir within the project")
					c.StrOpt(&jobRunOpts.prompt, "prompt", "", "", "prompt text (alternative to the positional)")
					c.IntOpt(&jobRunOpts.timeout, "timeout", "", 0, "job timeout in seconds (0 = server default)")
					c.StrOpt(&jobRunOpts.title, "title", "", "", "optional job title")
					c.BoolOpt(&jobRunOpts.wait, "wait", "", false, "poll until the job reaches a terminal state")
					c.AddArg("prompt", "prompt text passed to the agent", false)
				},
				Func: runJobRun,
			},
			{
				Name: "show",
				Desc: "Query a job's status",
				Config: func(c *gcli.Command) {
					c.StrOpt(&jobCommonOpts.showConfig, "config", "c", "", "path to the bridge config file")
					c.StrOpt(&jobCommonOpts.showServer, "server", "s", "", "server address (overrides config server.addr)")
					c.StrOpt(&jobCommonOpts.showToken, "token", "", "", "bearer token override (prefer config/env)")
					c.AddArg("id", "job id", true)
				},
				Func: runJobShow,
			},
			{
				Name: "logs",
				Desc: "Read a job's stdout/stderr logs",
				Config: func(c *gcli.Command) {
					c.StrOpt(&jobCommonOpts.logsConfig, "config", "c", "", "path to the bridge config file")
					c.StrOpt(&jobCommonOpts.logsServer, "server", "s", "", "server address (overrides config server.addr)")
					c.StrOpt(&jobCommonOpts.logsToken, "token", "", "", "bearer token override (prefer config/env)")
					c.StrOpt(&jobCommonOpts.logsStream, "stream", "", "stdout", "log stream: stdout|stderr")
					c.AddArg("id", "job id", true)
				},
				Func: runJobLogs,
			},
			{
				Name: "cancel",
				Desc: "Cancel a running job",
				Config: func(c *gcli.Command) {
					c.StrOpt(&jobCommonOpts.cancelConfig, "config", "c", "", "path to the bridge config file")
					c.StrOpt(&jobCommonOpts.cancelServer, "server", "s", "", "server address (overrides config server.addr)")
					c.StrOpt(&jobCommonOpts.cancelTkn, "token", "", "", "bearer token override (prefer config/env)")
					c.AddArg("id", "job id", true)
				},
				Func: runJobCancel,
			},
		},
	}
}

// newClient loads the config and builds an HTTP client. The server address
// priority is: --server flag > server.addr from config (plan §9-P6). The token
// is resolved from config/env (and an optional --token override) so it never
// needs to appear in shell history. A bare host:port is normalised and
// 0.0.0.0 -> 127.0.0.1 (see client.NormalizeBaseURL).
func newClient(configPath, serverFlag, tokenFlag string) (*client.Client, error) {
	cfg, _, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	addr := cfg.Server.Addr
	if serverFlag != "" {
		addr = serverFlag
	}
	if addr == "" {
		return nil, fmt.Errorf("no server address: pass --server or set server.addr in config")
	}
	token := resolveClientToken(&cfg.Server, tokenFlag)
	return client.New(addr, token), nil
}

// resolveClientToken mirrors serve's token resolution for the client side:
// server.token, overridden by server.token_env (when set and non-empty), then by
// an explicit --token flag.
func resolveClientToken(sc *config.ServerConfig, flagToken string) string {
	token := sc.Token
	if sc.TokenEnv != "" {
		if v := os.Getenv(sc.TokenEnv); v != "" {
			token = v
		}
	}
	if flagToken != "" {
		token = flagToken
	}
	return token
}

// argID returns the required <id> positional, preferring the gcli-bound named
// arg and falling back to the raw args slice (used by direct-call unit tests).
func argID(c *gcli.Command, args []string) string {
	if c != nil {
		if a := c.Arg("id"); a != nil && a.String() != "" {
			return a.String()
		}
	}
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

func runJobRun(c *gcli.Command, args []string) error {
	if jobRunOpts.project == "" {
		return fmt.Errorf("--project/-p is required")
	}
	if jobRunOpts.agent == "" {
		return fmt.Errorf("--agent/-a is required")
	}

	// prompt: prefer --prompt flag, else the positional <prompt> arg.
	prompt := jobRunOpts.prompt
	if prompt == "" {
		if c != nil {
			if a := c.Arg("prompt"); a != nil {
				prompt = a.String()
			}
		}
		if prompt == "" && len(args) > 0 {
			prompt = args[0]
		}
	}

	req := job.JobRequest{
		ProjectKey: jobRunOpts.project,
		Agent:      jobRunOpts.agent,
		Runner:     jobRunOpts.runner,
		Prompt:     prompt,
		Cmd:        rawCmd, // tokens after `--`, e.g. ["go","version"]
		Cwd:        jobRunOpts.cwd,
		TimeoutSec: jobRunOpts.timeout,
		Title:      jobRunOpts.title,
	}

	cli, err := newClient(jobRunOpts.config, jobRunOpts.server, jobRunOpts.token)
	if err != nil {
		return err
	}
	res, err := cli.SubmitJob(req)
	if err != nil {
		return err
	}
	c.Printf("job %s submitted: status=%s result_dir=%s\n", res.ID, res.Status, res.ResultDir)

	if jobRunOpts.wait {
		final, err := waitTerminal(cli, res.ID)
		if err != nil {
			return err
		}
		c.Printf("job %s finished: status=%s exit_code=%d\n", final.ID, final.Status, final.ExitCode)
	}
	return nil
}

// waitTerminal polls GetJob until the job reaches a terminal state.
func waitTerminal(cli *client.Client, id string) (job.JobResult, error) {
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		res, err := cli.GetJob(id)
		if err != nil {
			return res, err
		}
		switch res.Status {
		case job.StatusDone, job.StatusFailed, job.StatusCancelled, job.StatusTimeout:
			return res, nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return job.JobResult{}, fmt.Errorf("job %s did not finish within the wait window", id)
}

func runJobShow(c *gcli.Command, args []string) error {
	id := argID(c, args)
	if id == "" {
		return fmt.Errorf("job show requires an <id> argument")
	}
	cli, err := newClient(jobCommonOpts.showConfig, jobCommonOpts.showServer, jobCommonOpts.showToken)
	if err != nil {
		return err
	}
	res, err := cli.GetJob(id)
	if err != nil {
		return err
	}
	c.Printf("id:         %s\n", res.ID)
	c.Printf("project:    %s\n", res.ProjectKey)
	c.Printf("agent:      %s\n", res.Agent)
	c.Printf("runner:     %s\n", res.Runner)
	c.Printf("status:     %s\n", res.Status)
	c.Printf("exit_code:  %d\n", res.ExitCode)
	c.Printf("cwd:        %s\n", res.Cwd)
	c.Printf("result_dir: %s\n", res.ResultDir)
	if res.Error != "" {
		c.Printf("error:      %s\n", res.Error)
	}
	return nil
}

func runJobLogs(c *gcli.Command, args []string) error {
	id := argID(c, args)
	if id == "" {
		return fmt.Errorf("job logs requires an <id> argument")
	}
	stream := jobCommonOpts.logsStream
	if stream == "" {
		stream = "stdout"
	}
	cli, err := newClient(jobCommonOpts.logsConfig, jobCommonOpts.logsServer, jobCommonOpts.logsToken)
	if err != nil {
		return err
	}
	out, err := cli.GetLogs(id, stream)
	if err != nil {
		return err
	}
	c.Print(out)
	return nil
}

func runJobCancel(c *gcli.Command, args []string) error {
	id := argID(c, args)
	if id == "" {
		return fmt.Errorf("job cancel requires an <id> argument")
	}
	cli, err := newClient(jobCommonOpts.cancelConfig, jobCommonOpts.cancelServer, jobCommonOpts.cancelTkn)
	if err != nil {
		return err
	}
	res, err := cli.CancelJob(id)
	if err != nil {
		return err
	}
	c.Printf("job %s cancel requested: status=%s\n", res.ID, res.Status)
	return nil
}
