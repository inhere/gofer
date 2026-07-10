package commands

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/gookit/cliui/show/table"
	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/project"
)

// jobRunOpts holds `job run` flags. prompt is supplied via the --prompt flag
// (for cli-agents); exec argv comes from the tokens after `--`, which gcli hands
// to the Func handler as remainArgs (see runJobRun). The config path is the
// app-level global -c (config.InputCfgFile), not a per-command flag (P1).
var jobRunOpts = struct {
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
	plan         string
	channel      string
	role         string
	systemPrompt string
	agentArgs    gcli.Strings
	interactive  bool
	cols         int
	rows         int
}{}

// jobCommonOpts holds non-connection flags shared by show/logs/cancel (the
// --server/--token connection flags live in the shared jobConnOpts).
var jobCommonOpts = struct {
	logsStream string
}{}

// jobConnOpts holds the --server/--token connection flags shared by EVERY `job`
// AND `workflow` subcommand (bound via bindServerFlags). --server reads
// GOFER_SERVER_ADDR via gcli ${ENV} interpolation; --token's GOFER_SERVER_TOKEN
// fallback is resolved at runtime in resolveClientToken (NOT via the flag default,
// which gcli would render into --help and leak). Either lets a node submit without
// a config.yaml; an explicit flag or config server.addr still applies (newClient).
var jobConnOpts = struct{ server, token string }{}

// bindServerFlags binds the shared --server/-s and --token connection flags onto a
// subcommand (mirrors bindConfigFlag for -c). Every `job` and `workflow` subcommand
// calls it so the connection env defaults apply uniformly. --server keeps the
// ${GOFER_SERVER_ADDR} env default (addr is not secret; do NOT switch it to an empty
// default — that drops the env fallback, see E38③). --token uses an EMPTY default on
// purpose: a ${GOFER_SERVER_TOKEN} default leaks the token into --help (xu64.1); its
// env fallback lives in resolveClientToken instead.
func bindServerFlags(c *gcli.Command) {
	c.StrOpt(&jobConnOpts.server, "server", "s", "${GOFER_SERVER_ADDR}", "server address (overrides config server.addr)")
	c.StrOpt(&jobConnOpts.token, "token", "", "", "bearer token override (prefer config/env: GOFER_SERVER_TOKEN)")
}

// jobListOpts holds `job list` filter dimensions (mapped 1:1 onto job.ListOpts).
var jobListOpts = struct {
	project, status string
	caller, tag     string
	agent, runner   string
	session         string
	plan            string
	sourceJob       string
	since           int
	limit           int
}{}

// jobWatchOpts / jobRerunOpts hold `job watch` / `job rerun` flags.
var jobWatchOpts = struct {
	from int
}{}

var jobRerunOpts = struct {
	watch bool
}{}

// jobResumeOpts holds `job resume` flags (session-capture P2). prompt is the new
// turn's text; runner optionally pins the target runner (must equal the source
// job's runner — 同 runner 约束 — else the server rejects it).
var jobResumeOpts = struct {
	prompt string
	runner string
}{}

// NewJobCmd builds the `job` command group (run/show/logs/cancel). It wraps the
// server's /v1/jobs HTTP API so the host can drive jobs without curl (plan §9-P6).
func NewJobCmd() *gcli.Command {
	return &gcli.Command{
		Name: "job",
		Desc: "Submit and manage jobs via the bridge server",
		Subs: []*gcli.Command{
			{
				Name:    "run",
				Desc:    "Submit a new job",
				Aliases: []string{"add"},
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
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
					c.StrOpt(&jobRunOpts.plan, "plan", "", "", "attach the job to a plan (grouping key)")
					c.StrOpt(&jobRunOpts.channel, "channel", "", "cli", "submission channel recorded as provenance (cli/web/mcp/...)")
					c.StrOpt(&jobRunOpts.role, "role", "", "", "role preset (E35): fills agent/system_prompt/project/tags when unset")
					c.StrOpt(&jobRunOpts.systemPrompt, "system-prompt", "", "", "resident system prompt injected via the agent (advanced; overrides role's)")
					c.VarOpt(&jobRunOpts.agentArgs, "agent-arg", "", "extra arg appended to cli-agent argv (repeatable)")
					c.BoolOpt(&jobRunOpts.interactive, "interactive", "", false, "request an interactive pty job")
					c.IntOpt(&jobRunOpts.cols, "cols", "", 0, "initial terminal columns for --interactive (0 = server default 80)")
					c.IntOpt(&jobRunOpts.rows, "rows", "", 0, "initial terminal rows for --interactive (0 = server default 24)")
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
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "job id", true)
				},
				Func: runJobShow,
			},
			{
				Name: "logs",
				Desc: "Read a job's stdout/stderr logs",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&jobCommonOpts.logsStream, "stream", "", "stdout", "log stream: stdout|stderr")
					c.AddArg("id", "job id", true)
				},
				Func: runJobLogs,
			},
			{
				Name: "cancel",
				Desc: "Cancel a running job",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "job id", true)
				},
				Func: runJobCancel,
			},
			{
				Name:    "list",
				Desc:    "List jobs with optional filters (tag/agent/runner/since/...)",
				Aliases: []string{"ls"},
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&jobListOpts.project, "project", "p", "", "filter by project key")
					c.StrOpt(&jobListOpts.status, "status", "", "", "filter by status (queued/running/done/failed/cancelled/timeout)")
					c.StrOpt(&jobListOpts.caller, "caller", "", "", "filter by caller id")
					c.StrOpt(&jobListOpts.tag, "tag", "", "", "filter by tag (exact element match)")
					c.StrOpt(&jobListOpts.agent, "agent", "a", "", "filter by agent key")
					c.StrOpt(&jobListOpts.runner, "runner", "", "", "filter by runner key")
					c.StrOpt(&jobListOpts.session, "session", "", "", "filter by session id (exact match; lists a session's turns)")
					c.StrOpt(&jobListOpts.plan, "plan", "", "", "filter by plan id (exact match)")
					c.StrOpt(&jobListOpts.sourceJob, "source-job", "", "", "filter by source job id (list jobs derived from it)")
					c.IntOpt(&jobListOpts.since, "since", "", 0, "keep jobs with started_at >= since (unix seconds)")
					c.IntOpt(&jobListOpts.limit, "limit", "", 0, "max jobs to return (0 = server default)")
				},
				Func: runJobList,
			},
			{
				Name: "watch",
				Desc: "Stream a job's status + logs live until it finishes",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.IntOpt(&jobWatchOpts.from, "from", "", 0, "resume stdout from a byte offset")
					c.AddArg("id", "job id", true)
				},
				Func: runJobWatch,
			},
			{
				Name: "rerun",
				Desc: "Re-submit a job from its original request (fresh idempotency key)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.BoolOpt(&jobRerunOpts.watch, "watch", "w", false, "watch the new job's stream until it finishes")
					c.AddArg("id", "source job id", true)
				},
				Func: runJobRerun,
			},
			{
				Name: "resume",
				Desc: "Resume a job's underlying agent session as a new job (same runner)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&jobResumeOpts.prompt, "prompt", "", "", "prompt text for the resumed turn")
					c.StrOpt(&jobResumeOpts.runner, "runner", "", "", "target runner (must equal the source job's runner; default = source runner)")
					c.AddArg("id", "source job id", true)
				},
				Func: runJobResume,
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
	cfg, path, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	// 配置文件缺失(path=="") 且未显式指定 server 时，ApplyDefaults 会静默回落到
	// DefaultAddr(0.0.0.0:8765)，导致请求失败时报出含糊的 connection refused/404，
	// 用户不知道根因是"没配置也没指定 server"。这里在构造 client 前 fail-fast 报清晰错误。
	// serverFlag 已经过 gcli 的 ${GOFER_SERVER_ADDR} 环境变量插值(含 dotenv 加载后的
	// GOFER_CONFIG_DIR/.env)，非空即视为"显式指定"，不误伤正常配置场景。
	if path == "" && serverFlag == "" {
		return nil, fmt.Errorf("未找到配置文件，且未通过 -s/--server 指定 server 地址；" +
			"请用 -s/--server 指定，或配置 $GOFER_CONFIG_DIR/.env（或 --config 指定配置文件）")
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

// resolveClientToken resolves the bearer token for client-side calls.
// Precedence (highest first): explicit --token flag > GOFER_SERVER_TOKEN env >
// server.token_env > server.token. The GOFER_SERVER_TOKEN env fallback used to be
// carried via the --token flag's ${ENV} default, but gcli interpolates that into
// --help and leaks the token (xu64.1); it is resolved here at runtime instead.
func resolveClientToken(sc *config.ServerConfig, flagToken string) string {
	if flagToken != "" {
		return flagToken
	}
	if v := os.Getenv("GOFER_SERVER_TOKEN"); v != "" {
		return v
	}
	token := sc.Token
	if sc.TokenEnv != "" {
		if v := os.Getenv(sc.TokenEnv); v != "" {
			token = v
		}
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
	autoDetectJobProject(c)

	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
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

// autoDetectJobProject mirrors the `job run` D7 convenience: only when -p is
// absent, resolve the current directory to a configured project and relative cwd.
// Schedule add reuses it before building the stored JobRequest template.
func autoDetectJobProject(c *gcli.Command) {
	if jobRunOpts.project != "" {
		return
	}
	if cfg, _, err := config.Load(config.InputCfgFile); err == nil {
		if abs, e := filepath.Abs("."); e == nil {
			if key, rel, found := project.ResolveByCwd(cfg, abs); found {
				jobRunOpts.project = key
				if jobRunOpts.cwd == "" || jobRunOpts.cwd == "." {
					jobRunOpts.cwd = rel
				}
				c.Printf("auto-detected project %q (cwd=%s)\n", key, rel)
			}
		}
	}
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
	req, err := buildJobRunRequest(c, cli)
	if err != nil {
		return client.SubmitResult{}, err
	}
	return cli.SubmitJobSync(req)
}

// buildJobRunRequest maps the `job run` flag state and post-`--` argv into the
// JobRequest wire type. `schedule add` reuses it so scheduled jobs accept the
// same request flags as an immediate job run.
func buildJobRunRequest(c *gcli.Command, cli *client.Client) (job.JobRequest, error) {
	if err := validateJobRunRequired(); err != nil {
		return job.JobRequest{}, err
	}
	var cmd []string
	if a := c.Arg("cmd"); a != nil {
		cmd = a.Strings()
	}
	channel := jobRunOpts.channel
	if channel == "" {
		channel = "cli"
	}
	req := job.JobRequest{
		ProjectKey:     jobRunOpts.project,
		Agent:          jobRunOpts.agent,
		Runner:         jobRunOpts.runner,
		Prompt:         jobRunOpts.prompt,
		AgentArgs:      []string(jobRunOpts.agentArgs),
		Cmd:            cmd, // tokens after `--`, e.g. ["go","version"]
		Cwd:            jobRunOpts.cwd,
		TimeoutSec:     jobRunOpts.timeout,
		Title:          jobRunOpts.title,
		Sync:           jobRunOpts.sync,
		WaitTimeoutSec: jobRunOpts.waitTimeout,
		WorkerID:       jobRunOpts.workerID,
		WorkerLabels:   splitLabels(jobRunOpts.workerLabels),
		Tags:           splitLabels(jobRunOpts.tags), // comma-separated, same parsing as worker-labels
		PlanID:         jobRunOpts.plan,
		Interactive:    jobRunOpts.interactive,
		Cols:           jobRunOpts.cols,
		Rows:           jobRunOpts.rows,
		// 提交来源（provenance）：CLI 渠道(默认 cli，可 --channel 覆盖) + 本机 hostname。
		// server 端若 client 为空会以 remote IP 兜底盖章。
		Channel: channel,
		Client:  cliHostname(),
		// E35 role preset + optional system prompt override (resolved server-side).
		Role:         jobRunOpts.role,
		SystemPrompt: jobRunOpts.systemPrompt,
	}
	return req, nil
}

func validateJobRunRequired() error {
	// --role (E35) fills agent/project server-side, so they are not required when a
	// role is given (the server rejects an unknown role / still-missing fields).
	if jobRunOpts.role != "" {
		return nil
	}
	if jobRunOpts.project == "" {
		return fmt.Errorf("--project/-p is required (or pass --role)")
	}
	if jobRunOpts.agent == "" {
		return fmt.Errorf("--agent/-a is required (or pass --role)")
	}
	return nil
}

// cliHostname returns os.Hostname() for stamping a CLI submission's Client
// (provenance). A lookup failure yields "" — the server then falls back to the
// remote IP, so provenance is never wholly lost.
func cliHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
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
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
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
	// 提交来源（provenance）：渠道 / 来源主机|IP / 鉴权身份——回答"谁/哪台/经哪渠道提交"。
	if res.Channel != "" {
		c.Printf("channel:    %s\n", res.Channel)
	}
	if res.Client != "" {
		c.Printf("client:     %s\n", res.Client)
	}
	if res.CallerID != "" {
		c.Printf("caller_id:  %s\n", res.CallerID)
	}
	if res.SessionID != "" {
		c.Printf("session_id: %s\n", res.SessionID)
	}
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
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
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
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
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

// runJobList queries GET /v1/jobs with the bound filters and renders a table via
// gcli show/table (column widths are computed by the component, incl. CJK). It
// surfaces submission provenance (CHANNEL/CLIENT) so the listing answers "who /
// where / how submitted". An empty result prints a friendly hint.
func runJobList(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	jobs, err := cli.ListJobs(job.ListOpts{
		Project:   jobListOpts.project,
		Status:    jobListOpts.status,
		Caller:    jobListOpts.caller,
		Tag:       jobListOpts.tag,
		Agent:     jobListOpts.agent,
		Runner:    jobListOpts.runner,
		Session:   jobListOpts.session,
		Plan:      jobListOpts.plan,
		SourceJob: jobListOpts.sourceJob,
		Since:     int64(jobListOpts.since),
		Limit:     jobListOpts.limit,
	})
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		c.Println("no jobs matched the given filters")
		return nil
	}
	// gookit/cliui show/table 原生按显示宽度对齐(含 CJK，实测中文标题列对齐)+ ColMaxWidth
	// 截断，无需手工 padding。CHANNEL/CLIENT 为提交来源(provenance)。
	tb := table.New("", table.WithColMaxWidth(30))
	tb.SetHeads("ID", "TITLE", "STATUS", "CHANNEL", "CLIENT", "AGENT", "RUNNER", "PROJECT", "TAGS", "STARTED")
	for _, j := range jobs {
		tb.AddRow(j.ID, j.Title, j.Status, j.Channel, j.Client, j.Agent, j.Runner,
			j.ProjectKey, strings.Join(j.Tags, ","), formatStarted(j.StartedAt))
	}
	c.Print(tb.Render())
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
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}

	return watchToTerminal(c, cli, id, jobWatchOpts.from)
}

// watchToTerminal streams a job's SSE to terminal, printing status changes and
// raw log text, then maps the terminal status to a process exit code (done=0 /
// cancelled=130 / other=1) via os.Exit on the non-zero path. Ctrl-C cancels the
// stream and exits 130. Shared by `job watch` and `job rerun --watch`. The SSE
// watch state-machine lives in client.WatchJob (BP6); this keeps only the
// command presentation (printing + the exit-code mapping, a command concern).
func watchToTerminal(c *gcli.Command, cli *client.Client, id string, from int) error {
	// Ctrl-C cancels the stream context for a clean exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	finalStatus, streamErr := cli.WatchJob(ctx, id, from, client.WatchHandlers{
		OnStatus: func(status string) { c.Printf(">> status: %s\n", status) },
		OnLog:    func(text string) { c.Print(text) },
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

// runJobRerun re-submits <id> server-side via rebuild with empty overrides, then
// prints the new job id. With --watch it then streams the new job to terminal
// (reusing runJobWatch's path semantics by delegating to a fresh StreamJob).
func runJobRerun(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("job rerun requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	// P5: rerun = 服务端 rebuild 空 body（原样重投；env 全程不出服务端；血缘服务端盖 source_job_id）。
	res, err := cli.RebuildJob(id, job.RebuildOverrides{})
	if err != nil {
		return err
	}
	c.Printf("rerun of %s submitted: new job %s status=%s\n", id, res.ID, res.Status)

	if !jobRerunOpts.watch {
		return nil
	}
	return watchToTerminal(c, cli, res.ID, 0)
}

// runJobResume续接 the source job's底层 agent 会话 (session-capture P2): it POSTs
// to /v1/jobs/{id}/resume (server-side编排 in job.Service.ResumeJob) and prints
// the new job id. Default async (design §10-2: claude 慢任务 sync 会超时) — it
// prints a `gofer job watch <id>` hint rather than polling.
func runJobResume(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("job resume requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	res, err := cli.ResumeJob(id, jobResumeOpts.prompt, jobResumeOpts.runner)
	if err != nil {
		return err
	}
	c.Printf("resume of %s submitted: new job %s status=%s session_id=%s\n",
		id, res.ID, res.Status, res.SessionID)
	c.Printf("watch it: gofer job watch %s\n", res.ID)
	return nil
}
