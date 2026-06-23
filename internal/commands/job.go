package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
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
	tags         string
}{}

// jobCommonOpts holds the --config/--server/--token flags shared by
// show/logs/cancel.
var jobCommonOpts = struct {
	showConfig, showServer, showToken     string
	logsConfig, logsServer, logsToken     string
	logsStream                            string
	cancelConfig, cancelServer, cancelTkn string
}{}

// jobListOpts holds `job list` flags: the shared --config/--server/--token plus
// the E5 filter dimensions (mapped 1:1 onto job.ListOpts).
var jobListOpts = struct {
	config, server, token string
	project, status       string
	caller, tag           string
	agent, runner         string
	since                 int
	limit                 int
}{}

// jobWatchOpts / jobRerunOpts hold `job watch` / `job rerun` flags.
var jobWatchOpts = struct {
	config, server, token string
	from                  int
}{}

var jobRerunOpts = struct {
	config, server, token string
	watch                 bool
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
					c.StrOpt(&jobRunOpts.tags, "tags", "", "", "comma-separated free-form tags for the job (E5 search dimension, e.g. --tags ci,nightly)")
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
			{
				Name: "list",
				Desc: "List jobs with optional filters (E5: tag/agent/runner/since/...)",
				Config: func(c *gcli.Command) {
					c.StrOpt(&jobListOpts.config, "config", "c", "", "path to the bridge config file")
					c.StrOpt(&jobListOpts.server, "server", "s", "", "server address (overrides config server.addr)")
					c.StrOpt(&jobListOpts.token, "token", "", "", "bearer token override (prefer config/env)")
					c.StrOpt(&jobListOpts.project, "project", "p", "", "filter by project key")
					c.StrOpt(&jobListOpts.status, "status", "", "", "filter by status (queued/running/done/failed/cancelled/timeout)")
					c.StrOpt(&jobListOpts.caller, "caller", "", "", "filter by caller id")
					c.StrOpt(&jobListOpts.tag, "tag", "", "", "filter by tag (exact element match)")
					c.StrOpt(&jobListOpts.agent, "agent", "a", "", "filter by agent key")
					c.StrOpt(&jobListOpts.runner, "runner", "", "", "filter by runner key")
					c.IntOpt(&jobListOpts.since, "since", "", 0, "keep jobs with started_at >= since (unix seconds)")
					c.IntOpt(&jobListOpts.limit, "limit", "", 0, "max jobs to return (0 = server default)")
				},
				Func: runJobList,
			},
			{
				Name: "watch",
				Desc: "Stream a job's status + logs live until it finishes",
				Config: func(c *gcli.Command) {
					c.StrOpt(&jobWatchOpts.config, "config", "c", "", "path to the bridge config file")
					c.StrOpt(&jobWatchOpts.server, "server", "s", "", "server address (overrides config server.addr)")
					c.StrOpt(&jobWatchOpts.token, "token", "", "", "bearer token override (prefer config/env)")
					c.IntOpt(&jobWatchOpts.from, "from", "", 0, "resume stdout from a byte offset")
					c.AddArg("id", "job id", true)
				},
				Func: runJobWatch,
			},
			{
				Name: "rerun",
				Desc: "Re-submit a job from its original request (fresh idempotency key)",
				Config: func(c *gcli.Command) {
					c.StrOpt(&jobRerunOpts.config, "config", "c", "", "path to the bridge config file")
					c.StrOpt(&jobRerunOpts.server, "server", "s", "", "server address (overrides config server.addr)")
					c.StrOpt(&jobRerunOpts.token, "token", "", "", "bearer token override (prefer config/env)")
					c.BoolOpt(&jobRerunOpts.watch, "watch", "w", false, "watch the new job's stream until it finishes")
					c.AddArg("id", "source job id", true)
				},
				Func: runJobRerun,
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

// resolveProjectByCwd returns the project key whose host_path or container_path
// is the longest prefix of absCwd, plus cwd relative to that root (D7). It only
// uses the 注册锚 (host/container path), never the overlay. ok=false on zero match
// or a tie (两个项目同长前缀 → 让用户显式 -p, 避免误派).
func resolveProjectByCwd(cfg *config.Config, absCwd string) (key, relCwd string, ok bool) {
	bestLen := -1
	tie := false
	for k, p := range cfg.Projects {
		for _, root := range []string{p.ContainerPath, p.HostPath} {
			if root == "" {
				continue
			}
			abs, err := filepath.Abs(root)
			if err != nil {
				continue
			}
			if absCwd == abs || strings.HasPrefix(absCwd, abs+string(filepath.Separator)) {
				if len(abs) > bestLen {
					bestLen, key, tie = len(abs), k, false
					if rel, e := filepath.Rel(abs, absCwd); e == nil {
						relCwd = rel
					}
				} else if len(abs) == bestLen && k != key {
					tie = true
				}
			}
		}
	}
	if bestLen < 0 || tie {
		return "", "", false
	}
	if relCwd == "" {
		relCwd = "."
	}
	return key, relCwd, true
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
	// cwd→project auto-detect (D7): only when -p is absent. An explicit -p is never
	// overridden (cross-project submits must not be misrouted). Best-effort: any
	// load/abs failure silently leaves project empty → the existing
	// `--project/-p is required` error downstream (submitJSONJob).
	if jobRunOpts.project == "" {
		if cfg, _, err := config.Load(jobRunOpts.config); err == nil {
			if abs, e := filepath.Abs("."); e == nil {
				if key, rel, found := resolveProjectByCwd(cfg, abs); found {
					jobRunOpts.project = key
					if jobRunOpts.cwd == "" || jobRunOpts.cwd == "." {
						jobRunOpts.cwd = rel
					}
					c.Printf("auto-detected project %q (cwd=%s)\n", key, rel)
				}
			}
		}
	}

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
		Tags:           splitLabels(jobRunOpts.tags), // comma-separated, same parsing as worker-labels
	}
	return cli.SubmitJobSync(req)
}

// splitLabels parses a comma-separated flag value (--worker-labels, --tags) into
// a trimmed, non-empty slice (returns nil for an empty/whitespace-only input so
// the JobRequest omits the field).
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

// runJobList queries GET /v1/jobs with the bound filters and prints a fixed-width
// table (ID/STATUS/AGENT/RUNNER/PROJECT/TAGS/STARTED). An empty result prints a
// friendly hint instead of an empty table.
func runJobList(c *gcli.Command, _ []string) error {
	cli, err := newClient(jobListOpts.config, jobListOpts.server, jobListOpts.token)
	if err != nil {
		return err
	}
	jobs, err := cli.ListJobs(job.ListOpts{
		Project: jobListOpts.project,
		Status:  jobListOpts.status,
		Caller:  jobListOpts.caller,
		Tag:     jobListOpts.tag,
		Agent:   jobListOpts.agent,
		Runner:  jobListOpts.runner,
		Since:   int64(jobListOpts.since),
		Limit:   jobListOpts.limit,
	})
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		c.Println("no jobs matched the given filters")
		return nil
	}
	c.Printf("%-22s %-10s %-10s %-8s %-12s %-20s %s\n",
		"ID", "STATUS", "AGENT", "RUNNER", "PROJECT", "TAGS", "STARTED")
	for _, j := range jobs {
		c.Printf("%-22s %-10s %-10s %-8s %-12s %-20s %s\n",
			j.ID, j.Status, j.Agent, j.Runner, j.ProjectKey,
			strings.Join(j.Tags, ","), formatStarted(j.StartedAt))
	}
	return nil
}

// formatStarted renders a unix-seconds started_at as a local timestamp; 0 (never
// started) renders as "-".
func formatStarted(sec int64) string {
	if sec <= 0 {
		return "-"
	}
	return time.Unix(sec, 0).Format("2006-01-02 15:04:05")
}

// runJobWatch streams a job's SSE (status + incremental logs) until it reaches a
// terminal state, printing status changes and raw log text. On a terminal status
// it maps the job state to a process exit code (done=0, cancelled=130, any other
// non-done terminal=1) so it is scriptable. Ctrl-C (SIGINT) cancels the stream
// and exits cleanly. The terminal exit-code mapping is applied via os.Exit on the
// non-zero path because gcli only derives exit codes from coded errors, and watch
// is the last thing the process does.
func runJobWatch(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("job watch requires an <id> argument")
	}
	cli, err := newClient(jobWatchOpts.config, jobWatchOpts.server, jobWatchOpts.token)
	if err != nil {
		return err
	}

	return watchToTerminal(c, cli, id, jobWatchOpts.from)
}

// watchToTerminal streams a job's SSE to terminal, printing status changes and
// raw log text, then maps the terminal status to a process exit code (done=0 /
// cancelled=130 / other=1) via os.Exit on the non-zero path. Ctrl-C cancels the
// stream and exits 130. Shared by `job watch` and `job rerun --watch`.
func watchToTerminal(c *gcli.Command, cli *client.Client, id string, from int) error {
	// Ctrl-C cancels the stream context for a clean exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var lastStatus, finalStatus string
	streamErr := cli.StreamJob(ctx, id, from, func(ev client.SSEEvent) {
		switch ev.Event {
		case "status":
			var jr job.JobResult
			if err := json.Unmarshal(ev.Data, &jr); err != nil {
				return
			}
			if jr.Status != lastStatus {
				lastStatus = jr.Status
				c.Printf(">> status: %s\n", jr.Status)
			}
			if job.IsTerminal(jr.Status) {
				finalStatus = jr.Status
			}
		case "log":
			var lf struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(ev.Data, &lf); err == nil && lf.Text != "" {
				c.Print(lf.Text)
			}
		}
	})
	if streamErr != nil {
		return streamErr
	}

	// Ctrl-C path: the stream ended because ctx was cancelled, not because the job
	// finished. Report and exit 130 (SIGINT convention) without claiming a status.
	if ctx.Err() != nil && finalStatus == "" {
		c.Println("\nwatch interrupted")
		os.Exit(130)
	}

	if finalStatus == "" {
		// Stream ended (EOF) without a terminal status frame: fetch authoritative.
		if res, err := cli.GetJob(id); err == nil {
			finalStatus = res.Status
		}
	}
	c.Printf("job %s finished: status=%s\n", id, finalStatus)
	if code := terminalExitCode(finalStatus); code != 0 {
		os.Exit(code)
	}
	return nil
}

// terminalExitCode maps a job terminal status to a process exit code: done=0,
// cancelled=130 (SIGINT convention), every other terminal (failed/timeout) or
// unknown=1. Aligns with the existing status constants in internal/job.
func terminalExitCode(status string) int {
	switch status {
	case job.StatusDone:
		return 0
	case job.StatusCancelled:
		return 130
	default:
		// failed / timeout / empty / non-terminal-unknown.
		return 1
	}
}

// runJobRerun reads the original JobRequest of <id>, clears its RequestID (D5: a
// rerun is a brand-new job, not an idempotent hit on the source), re-submits it
// and prints the new job id. With --watch it then streams the new job to terminal
// (reusing runJobWatch's path semantics by delegating to a fresh StreamJob).
func runJobRerun(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("job rerun requires an <id> argument")
	}
	cli, err := newClient(jobRerunOpts.config, jobRerunOpts.server, jobRerunOpts.token)
	if err != nil {
		return err
	}
	req, err := cli.GetJobRequest(id)
	if err != nil {
		return err
	}
	// D5: a rerun must not be deduped against the source job's idempotency key.
	req.RequestID = ""

	sub, err := cli.SubmitJobSync(req)
	if err != nil {
		return err
	}
	newID := sub.Job.ID
	c.Printf("rerun of %s submitted: new job %s status=%s\n", id, newID, sub.Job.Status)

	if !jobRerunOpts.watch {
		return nil
	}
	return watchToTerminal(c, cli, newID, 0)
}
