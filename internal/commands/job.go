package commands

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// jobRunOpts holds `job run` flags. prompt is supplied via the --prompt flag
// (for cli-agents); exec argv comes from the tokens after `--`, which gcli hands
// to the Func handler as remainArgs (see runJobRun).
var jobRunOpts = struct {
	config       string
	server       string
	token        string
	project      string
	agent        string
	runner       string
	cwd          string
	prompt       string
	timeout      int
	title        string
	wait         bool
	sync         bool
	waitTimeout  int
	file         string
	workerID     string
	workerLabels string
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
					c.StrOpt(&jobRunOpts.prompt, "prompt", "", "", "prompt text for cli-agent (use -- <argv...> for exec)")
					c.IntOpt(&jobRunOpts.timeout, "timeout", "", 0, "job timeout in seconds (0 = server default)")
					c.StrOpt(&jobRunOpts.title, "title", "", "", "optional job title")
					c.BoolOpt(&jobRunOpts.wait, "wait", "", false, "poll until the job reaches a terminal state")
					c.BoolOpt(&jobRunOpts.sync, "sync", "", false, "submit synchronously: server waits for terminal state, then returns")
					c.IntOpt(&jobRunOpts.waitTimeout, "wait-timeout", "", 0, "sync wait cap in seconds (0 = server default 30s)")
					c.StrOpt(&jobRunOpts.file, "file", "f", "", "submit a md+yaml task file (frontmatter params + prompt body)")
					c.StrOpt(&jobRunOpts.workerID, "worker-id", "", "", "target worker id for runner=worker (explicit routing)")
					c.StrOpt(&jobRunOpts.workerLabels, "worker-labels", "", "", "comma-separated labels to auto-select a worker (runner=worker, when --worker-id is unset)")
					// exec argv after `--`, e.g. `job run -a exec -- go version`.
					// Declared as an optional arrayed arg so gcli binds the post-`--`
					// tokens natively (HasArguments()=true also suppresses the spurious
					// "subcommand not found" notice a no-arg leaf would print).
					c.AddArg("cmd", "raw command for exec agent (after --)", false, true)
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

// argID returns the required <id> positional from the gcli-bound named arg.
// id is declared via AddArg(..., true), so gcli enforces presence; this only
// reads the bound value.
func argID(c *gcli.Command) string {
	if c != nil {
		if a := c.Arg("id"); a != nil {
			return a.String()
		}
	}
	return ""
}

// runJobRun submits a job. With -f/--file it reads a md+yaml task file and
// submits it as text/markdown (frontmatter params + prompt body). Otherwise
// prompt comes from --prompt (cli-agents) and exec argv comes from the arrayed
// `cmd` arg, i.e. the tokens after `--` (e.g. `job run -a exec -- go version`).
// --sync asks the server to wait for the terminal state; a 202/async fallback
// transparently degrades to client-side polling.
func runJobRun(c *gcli.Command, _ []string) error {
	cli, err := newClient(jobRunOpts.config, jobRunOpts.server, jobRunOpts.token)
	if err != nil {
		return err
	}

	var sub client.SubmitResult
	if jobRunOpts.file != "" {
		sub, err = submitMarkdownFile(c, cli)
	} else {
		sub, err = submitJSONJob(c, cli)
	}
	if err != nil {
		return err
	}
	res := sub.Job
	c.Printf("job %s submitted: status=%s result_dir=%s\n", res.ID, res.Status, res.ResultDir)

	// --wait (client polling) or a sync submit that fell back to async (202): poll
	// until terminal. A sync submit that completed server-side already returns the
	// final result, so no extra poll is needed.
	polled := false
	if jobRunOpts.wait || sub.Async {
		final, err := waitTerminal(cli, res.ID)
		if err != nil {
			return err
		}
		res = final
		polled = true
	}
	// Print the terminal line for any wait/sync flow (sync that finished
	// server-side, sync/md that fell back to polling, or --wait).
	if polled || jobRunOpts.sync || job.IsTerminal(res.Status) {
		c.Printf("job %s finished: status=%s exit_code=%d\n", res.ID, res.Status, res.ExitCode)
	}
	return nil
}

// submitMarkdownFile reads the -f task file and submits it as text/markdown. It
// rejects mixing -f with --prompt or a post-`--` argv (the file is the single
// source of params + prompt).
func submitMarkdownFile(c *gcli.Command, cli *client.Client) (client.SubmitResult, error) {
	if jobRunOpts.prompt != "" {
		return client.SubmitResult{}, fmt.Errorf("--file/-f and --prompt are mutually exclusive")
	}
	if a := c.Arg("cmd"); a != nil && len(a.Strings()) > 0 {
		return client.SubmitResult{}, fmt.Errorf("--file/-f and a post-`--` argv are mutually exclusive")
	}
	body, err := os.ReadFile(jobRunOpts.file)
	if err != nil {
		return client.SubmitResult{}, fmt.Errorf("read task file: %w", err)
	}
	return cli.SubmitMarkdown(body)
}

// submitJSONJob builds a JobRequest from flags + post-`--` argv and submits it
// as JSON. --project/--agent are required on this path.
func submitJSONJob(c *gcli.Command, cli *client.Client) (client.SubmitResult, error) {
	if jobRunOpts.project == "" {
		return client.SubmitResult{}, fmt.Errorf("--project/-p is required")
	}
	if jobRunOpts.agent == "" {
		return client.SubmitResult{}, fmt.Errorf("--agent/-a is required")
	}
	var cmd []string
	if a := c.Arg("cmd"); a != nil {
		cmd = a.Strings()
	}
	req := job.JobRequest{
		ProjectKey:     jobRunOpts.project,
		Agent:          jobRunOpts.agent,
		Runner:         jobRunOpts.runner,
		Prompt:         jobRunOpts.prompt,
		Cmd:            cmd, // tokens after `--`, e.g. ["go","version"]
		Cwd:            jobRunOpts.cwd,
		TimeoutSec:     jobRunOpts.timeout,
		Title:          jobRunOpts.title,
		Sync:           jobRunOpts.sync,
		WaitTimeoutSec: jobRunOpts.waitTimeout,
		WorkerID:       jobRunOpts.workerID,
		WorkerLabels:   splitLabels(jobRunOpts.workerLabels),
	}
	return cli.SubmitJobSync(req)
}

// splitLabels parses the comma-separated --worker-labels value into a trimmed,
// non-empty label slice (returns nil for an empty/whitespace-only input so the
// JobRequest omits the field).
func splitLabels(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if l := strings.TrimSpace(part); l != "" {
			out = append(out, l)
		}
	}
	return out
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

func runJobShow(c *gcli.Command, _ []string) error {
	id := argID(c)
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

func runJobLogs(c *gcli.Command, _ []string) error {
	id := argID(c)
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

func runJobCancel(c *gcli.Command, _ []string) error {
	id := argID(c)
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
