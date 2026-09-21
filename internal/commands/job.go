package commands

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/cliui/show/table"
	"github.com/gookit/gcli/v3"
	"github.com/gookit/gcli/v3/gflag"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/project"
)

// jobRunFlags is the `job run` flag surface. It is a NAMED type so tests (and any
// future caller) can reset the flag state with one literal instead of restating every
// field — restating it is how a newly added flag silently fell out of the tests'
// reset path.
type jobRunFlags struct {
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
	todo         string
	channel      string
	role         string
	systemPrompt string
	agentArgs    gcli.Strings
	interactive  bool
	cols         int
	rows         int
	worktree     bool
	worktreeBase string
	review       bool
	readOnly     bool
	exclusiveDir bool
	sharedDir    bool
	verify       string
	verifyTime   int
	noVerify     bool
	fallback     string
	noFallback   bool
	template     string
	templateVars gcli.Strings
	upload       gcli.Strings
	collect      gcli.Strings
}

// jobRunOpts holds `job run` flags. prompt is supplied via the --prompt flag
// (for cli-agents); exec argv comes from the tokens after `--`, which gcli hands
// to the Func handler as remainArgs (see runJobRun). The config path is the
// app-level global -c (config.InputCfgFile), not a per-command flag (P1).
var jobRunOpts = jobRunFlags{}

// jobCommonOpts holds non-connection flags shared by show/logs/cancel (the
// --server/--token connection flags live in the shared jobConnOpts).
var jobCommonOpts = struct {
	logsStream string
	logsStderr bool
	logsLines  int
	logsHead   bool
	logsTail   bool
}{}

// jobConnOpts holds the --server/--token connection flags shared by EVERY `job`
// AND `workflow` subcommand (bound via bindServerFlags). --server reads
// GOFER_SERVER_ADDR via gcli ${ENV} interpolation; --token's GOFER_SERVER_TOKEN
// fallback is resolved at runtime in resolveClientToken (NOT via the flag default,
// which gcli would render into --help and leak). Either lets a node submit without
// a config.yaml; an explicit flag or config server.addr still applies (newClient).
var jobConnOpts = struct{ server, token string }{}

// jobRunOptCategory applies a help category and an explicit default to options
// bound through gcli's *Opt2 helpers. Keeping both in one helper makes the
// category assignment happen while the option is registered, which is required
// for gcli to build grouped help sections.
func jobRunOptCategory(category string, def any) gflag.CliOptFn {
	return func(opt *gflag.CliOpt) {
		opt.Category = category
		opt.DefVal = def
	}
}

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
					bindJobRunFlags(c)
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
					c.StrOpt(&jobCommonOpts.logsStream, "stream", "", "", "log stream: stdout|stderr")
					c.BoolOpt(&jobCommonOpts.logsStderr, "stderr", "", false, "read stderr stream")
					c.IntOpt(&jobCommonOpts.logsLines, "lines", "n", 0, "number of lines (last N; first N with --head; default 20 with --head/--tail)")
					c.BoolOpt(&jobCommonOpts.logsHead, "head", "", false, "read first lines")
					c.BoolOpt(&jobCommonOpts.logsTail, "tail", "", false, "read last lines")
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
				Name: "accept",
				Desc: "Accept a job awaiting review (needs_review -> done)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&jobAcceptOpts.note, "note", "", "", "optional note recorded with the acceptance")
					c.AddArg("id", "job id", true)
				},
				Func: runJobAccept,
			},
			{
				Name: "reject",
				Desc: "Reject a job awaiting review (needs_review -> rejected); --resume continues it with the note",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&jobRejectOpts.note, "note", "", "", "why the delivery is refused (required; also the continuation's prompt with --resume)")
					c.BoolOpt(&jobRejectOpts.resume, "resume", "", false, "continue the work: start a new job with the note as its prompt")
					c.AddArg("id", "job id", true)
				},
				Func: runJobReject,
			},
			{
				Name: "review",
				Desc: "Print a job's acceptance材料 in one screen (status / review / verify / commits / usage / diff summary + report tail); --diff adds the full diff",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.IntOpt(&jobReviewOpts.tail, "tail", "", defaultReviewTailLines, "how many lines of the agent's final report (stdout tail) to print")
					c.BoolOpt(&jobReviewOpts.diff, "diff", "", false, "also print the full diff (the captured changes.diff)")
					c.AddArg("id", "job id", true)
				},
				Func: runJobReview,
			},
			{
				Name:    "list",
				Desc:    "List jobs with optional filters (tag/agent/runner/since/...)",
				Aliases: []string{"ls"},
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&jobListOpts.project, "project", "p", "", "filter by project key")
					c.StrOpt(&jobListOpts.status, "status", "", "", "filter by status (queued/running/recovering/pending_interaction/needs_review/done/failed/cancelled/timeout/rejected)")
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
			{
				Name: "interactions",
				Desc: "List a job's interactions (questions, choices, approval requests)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "job id", true)
				},
				Func: runJobInteractions,
			},
			{
				Name: "answer",
				Desc: "Answer a job's pending interaction (an approval takes one of its option ids)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "job id", true)
					c.AddArg("interaction-id", "interaction id (see `job interactions`)", true)
					c.AddArg("answer", "the answer; for an approval, one of the interaction's option ids", true)
				},
				Func: runJobAnswer,
			},
			newJobWorktreeCmd(),
		},
	}
}

// jobWorktreeOpts holds `job worktree ls/rm` flags (WT-01).
var jobWorktreeOpts = struct {
	project      string
	limit        int
	force        bool
	deleteBranch bool
}{}

// newJobWorktreeCmd builds the `job worktree` group: the cleanup/inspection surface
// for WT-01 managed worktrees. `job run --worktree` KEEPS its worktree (the branch
// is the deliverable), so an operator needs a way to see what is still lying around
// and to reclaim it once the branch is merged.
func newJobWorktreeCmd() *gcli.Command {
	return &gcli.Command{
		Name:    "worktree",
		Desc:    "List or remove the managed worktrees of --worktree jobs (WT-01)",
		Aliases: []string{"wt"},
		Subs: []*gcli.Command{
			{
				Name:    "ls",
				Desc:    "List managed worktrees of --worktree jobs (branch, commits ahead, dirty, merged)",
				Aliases: []string{"list"},
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&jobWorktreeOpts.project, "project", "p", "", "filter by project key")
					c.IntOpt(&jobWorktreeOpts.limit, "limit", "", 0, "max jobs to inspect (0 = server default)")
				},
				Func: runJobWorktreeList,
			},
			{
				Name:    "rm",
				Desc:    "Remove a job's managed worktree (refuses uncommitted changes without --force)",
				Aliases: []string{"remove", "delete"},
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.BoolOpt(&jobWorktreeOpts.force, "force", "", false, "discard uncommitted changes in the worktree")
					c.BoolOpt(&jobWorktreeOpts.deleteBranch, "delete-branch", "", false, "also delete the branch (the deliverable — only when it is safe to lose)")
					c.AddArg("id", "job id", true)
				},
				Func: runJobWorktreeRemove,
			},
		},
	}
}

// runJobWorktreeList prints one line per managed worktree of the (project-filtered)
// job list. Branch state is fetched per job from the server so it is LIVE — `ls`
// answers "which branches are still unmerged / still dirty", not "what did the job
// look like when it ended".
func runJobWorktreeList(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	listed, err := cli.ListJobs(job.ListOpts{Project: jobWorktreeOpts.project, Limit: jobWorktreeOpts.limit})
	if err != nil {
		return err
	}
	// Only --worktree jobs carry one; a job whose row has no path (removed already,
	// or a job predating WT-01) is skipped rather than shown empty.
	var rows []job.WorktreeStatus
	for _, j := range listed {
		if j.WorktreePath == "" {
			continue
		}
		st, err := cli.GetJobWorktree(j.ID)
		if err != nil {
			return fmt.Errorf("job %s: %w", j.ID, err)
		}
		rows = append(rows, st)
	}
	if len(rows) == 0 {
		c.Printf("no managed worktrees\n")
		return nil
	}
	c.Printf("%-14s %-28s %-26s %5s %-6s %-7s %s\n", "JOB", "PROJECT", "BRANCH", "AHEAD", "DIRTY", "MERGED", "PATH")
	for _, st := range rows {
		state := "present"
		if !st.Exists {
			state = "MISSING"
		}
		c.Printf("%-14s %-28s %-26s %5d %-6s %-7s %s (%s)\n",
			st.JobID, st.Project, st.Branch, st.CommitsAhead,
			yesNo(st.Dirty), yesNo(st.Merged), st.Path, state)
	}
	return nil
}

// runJobWorktreeRemove removes one job's managed worktree on the server (which runs
// the git command), then reports what happened. It does not re-list: the returned
// status is the post-removal state.
func runJobWorktreeRemove(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("job worktree rm requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	st, err := cli.RemoveJobWorktree(id, jobWorktreeOpts.force, jobWorktreeOpts.deleteBranch)
	if err != nil {
		return err
	}
	c.Printf("removed worktree %s\n", st.Path)
	if jobWorktreeOpts.deleteBranch {
		c.Printf("deleted branch  %s\n", st.Branch)
	} else if st.Branch != "" {
		// The branch is the deliverable and is kept by default — say so, with the
		// hint that removes it, so nobody assumes the job's commits were dropped.
		c.Printf("kept branch     %s (use --delete-branch once it is merged)\n", st.Branch)
	}
	return nil
}

// yesNo renders a boolean flag column in the `worktree ls` table.
func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// bindJobRunFlags registers the deliberately large `job run` option surface in
// help groups. Connection flags (--config/--server/--token) remain in the
// uncategorized section; the groups below describe the request itself.
func bindJobRunFlags(c *gcli.Command) {
	// Target: where the job should run and which project/agent it targets.
	c.StrOpt2(&jobRunOpts.project, "project,p", "project key (required)", jobRunOptCategory("Target", ""))
	c.StrOpt2(&jobRunOpts.agent, "agent,a", "agent key (required)", jobRunOptCategory("Target", ""))
	c.StrOpt2(&jobRunOpts.runner, "runner", "runner key (server = server-local; local is a compatibility alias)", jobRunOptCategory("Target", "server"))
	c.StrOpt2(&jobRunOpts.workerID, "worker-id", "target worker id for runner=worker (explicit routing)", jobRunOptCategory("Target", ""))
	c.StrOpt2(&jobRunOpts.workerLabels, "worker-labels", "comma-separated labels to auto-select a worker (runner=worker, when --worker-id is unset)", jobRunOptCategory("Target", ""))

	// Execution: command/prompt construction and execution policy.
	c.StrOpt2(&jobRunOpts.cwd, "cwd", "working dir within the project", jobRunOptCategory("Execution", "."))
	// WT-01: run in a managed git worktree (own branch gofer/<job-id>) so parallel
	// jobs stop sharing one checkout/index.
	c.BoolOpt2(&jobRunOpts.worktree, "worktree", "run in a managed git worktree of the project checkout (branch gofer/<job-id>, kept afterwards)", gflag.WithCategory("Execution"))
	c.StrOpt2(&jobRunOpts.worktreeBase, "worktree-base", "base ref for --worktree (default: the checkout's current HEAD)", jobRunOptCategory("Execution", ""))
	c.StrOpt2(&jobRunOpts.prompt, "prompt", "prompt text for cli-agent (use -- <argv...> for exec)", jobRunOptCategory("Execution", ""))
	c.StrOpt2(&jobRunOpts.file, "file,f", "submit a md+yaml task file (frontmatter params + prompt body)", jobRunOptCategory("Execution", ""))
	// SUP-01 P5：任务书模板——服务端把 <name>.md 渲染成 prompt（frontmatter 可给 agent/
	// timeout/tags/verify 等默认值），--var 供 {{变量}}，--prompt 是追加在模板正文后的补充。
	c.StrOpt2(&jobRunOpts.template, "template,t", "render the prompt from a task-book template (see `gofer template ls`)", jobRunOptCategory("Execution", ""))
	c.VarOpt(&jobRunOpts.templateVars, "var", "", "template variable k=v (repeatable; requires --template/-t)", gflag.WithCategory("Execution"))
	c.StrOpt2(&jobRunOpts.role, "role", "role preset (E35): fills agent/system_prompt/project/tags when unset", jobRunOptCategory("Execution", ""))
	c.StrOpt2(&jobRunOpts.systemPrompt, "system-prompt", "resident system prompt injected via the agent (advanced; overrides role's)", jobRunOptCategory("Execution", ""))
	c.VarOpt(&jobRunOpts.agentArgs, "agent-arg", "", "extra arg appended to cli-agent argv (repeatable)", gflag.WithCategory("Execution"))
	// bd h-aii-0ql3：只读 job（cli-agent 追加沙箱参数 / acp-agent session/set_mode）。
	c.BoolOpt2(&jobRunOpts.readOnly, "read-only", "run read-only: audit/analysis only, the agent cannot write (cli-agent read_only_args / acp-agent acp.modes.read_only)", gflag.WithCategory("Execution"))
	// JOB-11：同 cwd 串行锁的两个反转开关。默认规则 = 可写 agent job 独占其工作目录、
	// exec/只读 job 共享；两者都不给 = 交给 server 按默认规则判定（三态）。
	c.BoolOpt2(&jobRunOpts.exclusiveDir, "exclusive-dir", "force the exclusive same-directory lock for this job (an exec job included): no other exclusive job runs in this directory, its ancestors or its subdirectories", gflag.WithCategory("Execution"))
	c.BoolOpt2(&jobRunOpts.sharedDir, "shared-dir", "give up the exclusive same-directory lock for this job (share the tree with other jobs, at your own risk)", gflag.WithCategory("Execution"))
	// GATE-01 S3：人工验收——agent 正常完成后停在 needs_review，等人 accept/reject。
	c.BoolOpt2(&jobRunOpts.review, "review", "require human review: on a normal completion the job parks in needs_review until someone accepts or rejects it", gflag.WithCategory("Execution"))
	c.IntOpt2(&jobRunOpts.timeout, "timeout", "job timeout in seconds (0 = server default)", jobRunOptCategory("Execution", 0))
	// SUP-01 P2：验证步骤——agent 正常结束（exit 0）后在同一个 cwd/env 里跑的验收命令；
	// 失败即 job failed。CLI 收一整条命令行（shell-words 拆 argv；gofer 不经过 shell，
	// 需要 shell 就写成 --verify 'bash -lc "…"'）。
	c.StrOpt2(&jobRunOpts.verify, "verify", "command to run after the agent finishes (shell-words, no shell); a non-zero exit fails the job", jobRunOptCategory("Execution", ""))
	c.IntOpt2(&jobRunOpts.verifyTime, "verify-timeout", "timeout for the --verify step in seconds (0 = project default, 600s)", jobRunOptCategory("Execution", 0))
	c.BoolOpt2(&jobRunOpts.noVerify, "no-verify", "do not run the project's default verify step for this job", gflag.WithCategory("Execution"))
	// SUP-01 P3：故障转移——agent 因供应商错误挂掉时改派下一个候选（覆盖项目/agent 级配置）。
	c.StrOpt2(&jobRunOpts.fallback, "fallback", "comma-separated fallback agents for a transient failure (e.g. omp,claude); overrides the project/agent lists", jobRunOptCategory("Execution", ""))
	c.BoolOpt2(&jobRunOpts.noFallback, "no-fallback", "do not hand this job to a fallback agent (overrides every configured list)", gflag.WithCategory("Execution"))
	// XFER-01 X2：随 job 传文件——--upload 在提交前把本地文件暂存到 server，执行机在 agent
	// 开跑前放到 job 的 cwd 里（放不下即 job failed、agent 不启动）；--collect 在 job 结束
	// 后按 glob 在同一个 cwd 收文件，回传落进该 job 的 artifacts/collected/。
	c.VarOpt(&jobRunOpts.upload, "upload", "", "local file to place on the executing machine: <local path>:<dest relative to the job cwd> (repeatable)", gflag.WithCategory("Execution"))
	c.VarOpt(&jobRunOpts.collect, "collect", "", "glob collected from the job's cwd into its artifacts after the job ends (repeatable, e.g. 'tmp/out/*.csv')", gflag.WithCategory("Execution"))

	// Submission: provenance and grouping metadata.
	c.StrOpt2(&jobRunOpts.title, "title", "optional job title", jobRunOptCategory("Submission", ""))
	c.StrOpt2(&jobRunOpts.tags, "tags", "comma-separated free-form tags for the job (E5 search dimension, e.g. --tags ci,nightly)", jobRunOptCategory("Submission", ""))
	c.StrOpt2(&jobRunOpts.plan, "plan", "attach the job to a plan (grouping key)", jobRunOptCategory("Submission", ""))
	c.StrOpt2(&jobRunOpts.todo, "todo", "run this job for a plan todo: the plan is resolved from the todo, the item turns doing and its outcome/commits are written back to its note", jobRunOptCategory("Submission", ""))
	c.StrOpt2(&jobRunOpts.channel, "channel", "submission channel recorded as provenance (cli/web/mcp/...)", jobRunOptCategory("Submission", "cli"))

	// Wait: synchronous submission and client-side polling controls.
	c.BoolOpt2(&jobRunOpts.wait, "wait", "poll until the job reaches a terminal state", gflag.WithCategory("Wait"))
	c.BoolOpt2(&jobRunOpts.sync, "sync", "submit synchronously: server waits for terminal state, then returns", gflag.WithCategory("Wait"))
	c.IntOpt2(&jobRunOpts.waitTimeout, "wait-timeout", "sync wait cap in seconds (0 = server default 30s)", jobRunOptCategory("Wait", 0))

	// Interactive: pty-specific controls.
	c.BoolOpt2(&jobRunOpts.interactive, "interactive", "request an interactive pty job", gflag.WithCategory("Interactive"))
	c.IntOpt2(&jobRunOpts.cols, "cols", "initial terminal columns for --interactive (0 = server default 80)", jobRunOptCategory("Interactive", 0))
	c.IntOpt2(&jobRunOpts.rows, "rows", "initial terminal rows for --interactive (0 = server default 24)", jobRunOptCategory("Interactive", 0))

	// exec argv after `--`, e.g. `job run -a exec -- go version`.
	c.AddArg("cmd", "raw command for exec agent (after --)", false, true)
}

// newClient loads the config and builds an HTTP client. The server address
// priority is: --server flag > server.addr from config (plan §9-P6). The token
// is resolved from config/env (and an optional --token override) so it never
// needs to appear in shell history. A bare host:port is normalised and
// 0.0.0.0 -> 127.0.0.1 (see client.NormalizeBaseURL).
//
// In client mode (GOFER_RUN_MODE=client) there is no local config to point at, so
// a missing address is reported as the connection env the node is supposed to
// carry instead of the generic "no config file" hint (which would send the
// operator looking for a config.yaml a client node must not have).
func newClient(configPath, serverFlag, tokenFlag string) (*client.Client, error) {
	cfg, path, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	addr := cfg.Server.Addr
	if serverFlag != "" {
		addr = serverFlag
	}
	if config.IsClientRunMode() {
		// A client node's address arrives from --server (bound to the
		// ${GOFER_SERVER_ADDR} env default) or from an explicitly named --config;
		// with neither, ApplyDefaults' fallback address would point at a local
		// 0.0.0.0:8765 nobody is serving — so fail with the client-node hint.
		if path == "" && serverFlag == "" {
			return nil, fmt.Errorf("client mode: set GOFER_SERVER_ADDR/GOFER_SERVER_TOKEN in $GOFER_CONFIG_DIR/.env or pass -s/--token")
		}
		return client.New(addr, resolveClientToken(&cfg.Server, tokenFlag)), nil
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

// runJobInteractions lists a job's interactions (question/choice/confirmation and —
// for an acp-agent under an approval policy — the permission requests waiting for a
// human). Each permission line names the gated tool call, and every line lists the
// option ids an answer may name, so `job answer` can be driven straight from it.
func runJobInteractions(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("job interactions requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	list, err := cli.GetInteractions(id)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		c.Printf("job %s has no interactions\n", id)
		return nil
	}
	for _, it := range list {
		c.Printf("%s  %-8s %-12s %s\n", it.ID, it.Status, it.Type, interactionSummary(it))
		if opts := interactionOptions(it.Options); opts != "" {
			c.Printf("    options: %s\n", opts)
		}
	}
	return nil
}

// runJobAnswer answers one pending interaction. For a permission interaction the
// answer is one of the ACP option ids (`allow-once-id`, `reject-once-id`, …) — the
// agent's own option is relayed to it verbatim, so gofer never decides for the agent
// what a rejection means.
func runJobAnswer(c *gcli.Command, _ []string) error {
	id, iid := argID(c), argString(c, "interaction-id")
	answer := argString(c, "answer")
	if id == "" || iid == "" || answer == "" {
		return fmt.Errorf("job answer requires <id> <interaction-id> <answer>")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	it, err := cli.AnswerInteraction(id, iid, answer, "")
	if err != nil {
		return err
	}
	c.Printf("interaction %s answered: %s (status=%s, by=%s)\n", it.ID, it.Answer, it.Status, it.AnsweredBy)
	return nil
}

// interactionSummary renders an interaction's one-line description: for a permission
// request (GATE-01 §1) the gated tool call comes first — kind, title, the files it
// touches and the truncated raw input — followed by the prompt and the policy hint.
func interactionSummary(it job.Interaction) string {
	var parts []string
	if tc := it.ToolCall; tc != nil {
		tool := "tool call"
		if tc.Kind != "" {
			tool = "[" + tc.Kind + "]"
		}
		if tc.Title != "" {
			tool += " " + strconv.Quote(tc.Title)
		}
		if len(tc.Locations) > 0 {
			tool += " @" + strings.Join(tc.Locations, ",")
		}
		if tc.RawInputSummary != "" {
			tool += " input=" + tc.RawInputSummary
		}
		parts = append(parts, tool)
	}
	if it.Prompt != "" {
		parts = append(parts, it.Prompt)
	}
	if it.PolicyHint != "" {
		parts = append(parts, "("+it.PolicyHint+")")
	}
	if it.Answer != "" {
		parts = append(parts, "answer="+it.Answer)
	}
	return strings.Join(parts, " ")
}

// interactionOptions renders "label=value" pairs (label falls back to the value), so
// the ids to pass to `job answer` are visible without another call.
func interactionOptions(opts []job.InteractionOption) string {
	if len(opts) == 0 {
		return ""
	}
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		label := o.Label
		if label == "" {
			label = o.Value
		}
		out = append(out, label+"="+o.Value)
	}
	return strings.Join(out, ", ")
}

// argString reads a named positional argument ("" when absent).
func argString(c *gcli.Command, name string) string {
	if c != nil {
		if a := c.Arg(name); a != nil {
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
	guardInteractiveSync(c)

	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}

	if err := checkJobRunSources(c); err != nil {
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
	// bd h-aii-s9ck: the server clamps timeout_sec to the project ceiling. Say so on
	// stderr (never silently truncate a long job's budget) — the job will be killed
	// at res.TimeoutSec, not at what was asked for.
	if res.TimeoutClamped {
		fmt.Fprintf(os.Stderr, "warning: --timeout %ds exceeds the project ceiling (%ds); the job will run with %ds\n",
			res.RequestedTimeoutSec, res.TimeoutSec, res.TimeoutSec)
	}

	// --wait (client polling) or a sync submit that fell back to async (202): poll
	// until terminal. A sync submit that completed server-side already returns the
	// final result, so no extra poll is needed.
	polled := false
	if jobRunOpts.wait || sub.Async {
		final, err := waitTerminal(cli, res.ID, jobRunOpts.timeout)
		if err != nil {
			return err
		}
		res = final
		polled = true
	}
	// Print the terminal line for any wait/sync flow (sync that finished
	// server-side, sync/md that fell back to polling, or --wait).
	if polled || jobRunOpts.sync || job.IsFinished(res.Status) {
		c.Printf("job %s finished: status=%s exit_code=%d\n", res.ID, res.Status, res.ExitCode)
		if shouldPrintJobStderr(res) {
			if logs, e := cli.GetLogsWindow(res.ID, client.LogOpts{Stream: "stderr", Lines: 20}); e == nil && logs != "" {
				fmt.Fprint(os.Stderr, "--- stderr (last 20 lines) ---\n", logs)
			}
		}
	}
	return nil
}

func shouldPrintJobStderr(res job.JobResult) bool {
	return res.ExitCode != 0 || res.Status == job.StatusFailed || res.Status == job.StatusTimeout || res.Status == job.StatusCancelled
}

// guardInteractiveSync forces async submission for --interactive jobs (tools-l8p).
// Interactive jobs are resident pty sessions with no natural terminal state, so a
// --sync submit would block the POST response for up to the server wait cap
// (SubmitSync's 30-60s) before runJobRun ever reaches the "job %s submitted"
// print — starving scripts that need the id right away to watch/attach next.
func guardInteractiveSync(c *gcli.Command) {
	if jobRunOpts.interactive && jobRunOpts.sync {
		c.Println("note: --sync is ignored for --interactive (resident session); submitting async so the job id prints immediately")
		jobRunOpts.sync = false
	}
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
	// XFER-01 X2: the file steps are flags of this command (`--upload` stages through
	// the client, which the md path does not run); a task file carries them in its
	// frontmatter instead, where they are a static part of the request.
	if len(jobRunOpts.upload) > 0 || len(jobRunOpts.collect) > 0 {
		return client.SubmitResult{}, fmt.Errorf("--upload/--collect are not available with --file/-f: put them in the task file's frontmatter")
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
	if err := stageJobUploads(c, cli, &req); err != nil {
		return client.SubmitResult{}, err
	}
	return cli.SubmitJobSync(req)
}

// stageJobUploads turns every --upload spec into a staged transfer and fills the
// request's uploads with the returned ids (XFER-01 X2). The push is stage_only: the
// job's EXECUTING machine places the file — at the job's own cwd — when the job
// starts, so dispatching it from here would put it at the wrong path.
//
// A staging failure aborts the submit: a job whose files could not be staged must not
// be created (the server would reject it anyway, and a silently dropped upload would
// run the agent against a cwd the caller did not describe).
func stageJobUploads(c *gcli.Command, cli *client.Client, req *job.JobRequest) error {
	if len(jobRunOpts.upload) == 0 {
		return nil
	}
	if req.ProjectKey == "" {
		return fmt.Errorf("--upload needs --project: the transfer is staged against a project")
	}
	runner := uploadRunner(req)
	for _, spec := range jobRunOpts.upload {
		local, dest, err := parseUploadSpec(spec)
		if err != nil {
			return err
		}
		rec, err := cli.XferStage(context.Background(), runner, req.ProjectKey, dest, local)
		if err != nil {
			return fmt.Errorf("stage %s: %w", local, err)
		}
		req.Uploads = append(req.Uploads, job.UploadSpec{XferID: rec.ID, Dest: dest})
		c.Printf("staged %s -> %s (%s)\n", local, dest, rec.ID)
	}
	return nil
}

// uploadRunner is the transfer runner an upload is staged against: the WORKER that
// will execute the job (a worker only serves transfers assigned to itself), else the
// server's own machine. --worker pins the worker id; without it the runner name IS the
// worker id for a worker runner — the same convention `gofer tool cp` uses — and the
// built-in local/server aliases normalize onto each other.
func uploadRunner(req *job.JobRequest) string {
	if id := strings.TrimSpace(jobRunOpts.workerID); id != "" {
		return id
	}
	return config.NormalizeRunnerName(req.Runner)
}

// parseUploadSpec splits an --upload value into `<local path>:<destination>`. The LAST
// colon is the separator: a Windows local path carries a drive letter
// (`D:\in\a.bin:tmp/a.bin`) that must stay part of the file, while a destination is a
// project-relative path and never contains one.
func parseUploadSpec(spec string) (local, dest string, err error) {
	i := strings.LastIndexByte(spec, ':')
	if i <= 0 || i == len(spec)-1 {
		return "", "", fmt.Errorf("--upload %q: want <local path>:<dest relative to the job cwd>", spec)
	}
	local, dest = strings.TrimSpace(spec[:i]), strings.TrimSpace(spec[i+1:])
	if local == "" || dest == "" {
		return "", "", fmt.Errorf("--upload %q: want <local path>:<dest relative to the job cwd>", spec)
	}
	return local, dest, nil
}

// buildJobRunRequest maps the `job run` flag state and post-`--` argv into the
// JobRequest wire type. `schedule add` reuses it so scheduled jobs accept the
// same request flags as an immediate job run.
func buildJobRunRequest(c *gcli.Command, cli *client.Client) (job.JobRequest, error) {
	if err := validateJobRunRequired(); err != nil {
		return job.JobRequest{}, err
	}
	if err := checkJobRunSources(c); err != nil {
		return job.JobRequest{}, err
	}
	var cmd []string
	if a := c.Arg("cmd"); a != nil {
		cmd = a.Strings()
	}
	tplVars, err := jobRunTemplateVars()
	if err != nil {
		return job.JobRequest{}, err
	}
	channel := jobRunOpts.channel
	if channel == "" {
		channel = "cli"
	}
	runner := normalizeJobRunner(jobRunOpts.runner)
	// SUP-01 P5：带 -t 时若调用方没钉 runner，就把它留给模板（模板默认 > 内置 local）。
	// gcli 无法区分"没给 --runner"与"给了 --runner server"（后者就是默认值），所以默认值
	// 本身充当哨兵——两种拼写都映射到同一个内置 local runner，唯一的差别正是"模板里写了
	// 另一个 runner"这一种情况，而那正是本分支存在的理由。
	if jobRunOpts.template != "" && jobRunOpts.runner == jobRunDefaultRunner {
		runner = ""
	}
	// SUP-01 P2: --verify takes ONE command line and the wire takes an argv, so the
	// split happens here (never later: gofer runs the argv verbatim). An empty or
	// unsplittable value is a usage error, not a silently dropped step.
	var verify []string
	if strings.TrimSpace(jobRunOpts.verify) != "" {
		words, verr := splitShellWords(jobRunOpts.verify)
		if verr != nil {
			return job.JobRequest{}, verr
		}
		if len(words) == 0 {
			return job.JobRequest{}, fmt.Errorf("--verify is empty")
		}
		verify = words
	}
	// JOB-11：同 cwd 独占的三态——两个都不给 = nil（server 按默认规则判定）。
	var exclusive *bool
	switch {
	case jobRunOpts.exclusiveDir && jobRunOpts.sharedDir:
		return job.JobRequest{}, fmt.Errorf("--exclusive-dir and --shared-dir are mutually exclusive")
	case jobRunOpts.exclusiveDir:
		v := true
		exclusive = &v
	case jobRunOpts.sharedDir:
		v := false
		exclusive = &v
	}
	req := job.JobRequest{
		ProjectKey:     jobRunOpts.project,
		Agent:          jobRunOpts.agent,
		Runner:         runner,
		Prompt:         jobRunOpts.prompt,
		AgentArgs:      []string(jobRunOpts.agentArgs),
		Cmd:            cmd, // tokens after `--`, e.g. ["go","version"]
		Cwd:            jobRunOpts.cwd,
		Worktree:       jobRunOpts.worktree,
		WorktreeBase:   jobRunOpts.worktreeBase,
		TimeoutSec:     jobRunOpts.timeout,
		Title:          jobRunOpts.title,
		Sync:           jobRunOpts.sync,
		WaitTimeoutSec: jobRunOpts.waitTimeout,
		WorkerID:       jobRunOpts.workerID,
		WorkerLabels:   splitLabels(jobRunOpts.workerLabels),
		Tags:           splitLabels(jobRunOpts.tags), // comma-separated, same parsing as worker-labels
		PlanID:         jobRunOpts.plan,
		TodoID:         jobRunOpts.todo,
		Interactive:    jobRunOpts.interactive,
		ReadOnly:       jobRunOpts.readOnly,
		// JOB-11：同 cwd 独占决策（nil = 交给 server 的默认规则）。
		ExclusiveDir: exclusive,
		// GATE-01 S3：人工验收（正常完成 → needs_review，等人 accept/reject）。
		Review: jobRunOpts.review,
		// SUP-01 P2：验证步骤（argv 已在此拆好）+ 它的独立超时 + 关闭项目默认的开关。
		Verify:           verify,
		VerifyTimeoutSec: jobRunOpts.verifyTime,
		NoVerify:         jobRunOpts.noVerify,
		// SUP-01 P3：本 job 的候选列表（覆盖项目/agent 级）+ 关闭开关。
		FallbackAgents: splitLabels(jobRunOpts.fallback),
		NoFallback:     jobRunOpts.noFallback,
		// SUP-01 P5：任务书模板 + 它的变量值（服务端渲染；两者随 request_json 存档）。
		Template:     jobRunOpts.template,
		TemplateVars: tplVars,
		// XFER-01 X2：collect glob 原样上报（执行机在 job 的 cwd 里匹配）；uploads 由
		// stageJobUploads 在提交前暂存后填进来（它要 client，拿得到 xfer id）。
		Collect: []string(jobRunOpts.collect),
		Cols:    jobRunOpts.cols,
		Rows:    jobRunOpts.rows,
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

// splitShellWords splits ONE command line into an argv the way a POSIX shell would
// do it for QUOTING only — no expansion, no globbing, no operators. It exists because
// `--verify` takes a command as a human writes it while the wire format (and the
// execution) is an argv: gofer never runs a shell, so the operator's intent has to be
// captured here rather than re-interpreted later.
//
// Rules: whitespace separates words; single quotes are literal; inside double quotes
// `\"` and `\\` escape; outside quotes a backslash escapes the next character; `”`
// and `""` produce an empty word. An unbalanced quote or a trailing backslash is an
// error — the CLI must refuse to guess an argv, not run something nobody wrote.
func splitShellWords(s string) ([]string, error) {
	var (
		out     []string
		cur     strings.Builder
		quote   rune
		started bool
		escaped bool
	)
	flush := func() {
		if started {
			out = append(out, cur.String())
			cur.Reset()
			started = false
		}
	}
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped = true
			started = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
			started = true
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if escaped {
		return nil, fmt.Errorf("--verify ends with a backslash: %q", s)
	}
	if quote != 0 {
		return nil, fmt.Errorf("--verify has an unbalanced %c quote: %q", quote, s)
	}
	flush()
	return out, nil
}

// jobRunDefaultRunner is the `--runner` flag's default value: the sentinel for
// "the caller did not pin a runner" (see buildJobRunRequest).
const jobRunDefaultRunner = "server"

// checkJobRunSources rejects an ambiguous prompt source combination: the markdown
// task file (-f), a task-book template (-t) and a post-`--` exec argv each define
// what the job IS, so at most one of them may name the work.
func checkJobRunSources(c *gcli.Command) error {
	hasArgv := false
	if a := c.Arg("cmd"); a != nil {
		hasArgv = len(a.Strings()) > 0
	}
	if jobRunOpts.file != "" && jobRunOpts.template != "" {
		return fmt.Errorf("--file/-f and --template/-t are mutually exclusive")
	}
	if jobRunOpts.template != "" && hasArgv {
		return fmt.Errorf("--template/-t and a post-`--` argv are mutually exclusive (a template renders a prompt, an exec job carries its own command)")
	}
	return nil
}

// jobRunTemplateVars parses the repeated --var k=v flags into the request's template
// variables. A --var without --template is a usage error: a value nobody renders
// would otherwise be dropped in silence.
func jobRunTemplateVars() (map[string]string, error) {
	if len(jobRunOpts.templateVars) == 0 {
		return nil, nil
	}
	if jobRunOpts.template == "" {
		return nil, fmt.Errorf("--var requires --template/-t")
	}
	return parseVarFlags(jobRunOpts.templateVars)
}

// parseVarFlags parses repeated `k=v` flags. The value may contain '=' (only the
// first one separates), and an empty value is allowed (`--var note=`).
func parseVarFlags(flags gcli.Strings) (map[string]string, error) {
	out := make(map[string]string, len(flags))
	for _, kv := range flags {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("--var expects k=v, got %q", kv)
		}
		out[k] = v
	}
	return out, nil
}

// normalizeJobRunner keeps the public CLI name unambiguous while preserving the
// server's existing wire identifier. `server` and the legacy `local` alias both
// mean the server process's built-in local runner; configured runner ids pass
// through unchanged.
//
// The alias vocabulary itself lives in config (config.NormalizeRunnerName) so the
// CLI and the server cannot drift apart; the CLI-only part is mapping its own
// flag DEFAULT (an empty value) to local, because for the CLI "no --runner" means
// the sentinel that buildJobRunRequest resolves against a task template.
func normalizeJobRunner(value string) string {
	if strings.TrimSpace(value) == "" {
		return config.BuiltinLocalRunner
	}
	return config.NormalizeRunnerName(value)
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
	// --template/-t (SUP-01 P5) renders the prompt and may carry the agent in its
	// frontmatter, so a missing --agent is not a usage error on that path; the server
	// rejects a template that names no agent either.
	if jobRunOpts.agent == "" && jobRunOpts.template == "" {
		return fmt.Errorf("--agent/-a is required (or pass --role/--template)")
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
// waitGrace is how long the client keeps polling past the job's own timeout.
const waitGrace = 2 * time.Minute

// waitDeadline bounds a client-side wait: the job's timeout plus waitGrace when
// the caller knows it, else the zero time (no client cap — the server's own
// timeout still drives the job to a terminal state).
func waitDeadline(now time.Time, timeoutSec int) time.Time {
	if timeoutSec <= 0 {
		return time.Time{}
	}
	return now.Add(time.Duration(timeoutSec)*time.Second + waitGrace)
}

// dirLockLabel renders a job's resolved same-directory lock policy (JOB-11) for
// `job show`: "exclusive" when the job holds (or would hold) the lock of its working
// directory, "shared" when it runs alongside whoever else is there.
func dirLockLabel(exclusive bool) string {
	if exclusive {
		return "exclusive"
	}
	return "shared"
}

func waitTerminal(cli *client.Client, id string, timeoutSec int) (job.JobResult, error) {
	deadline := waitDeadline(time.Now(), timeoutSec)
	for {
		res, err := cli.GetJob(id)
		if err != nil {
			return res, err
		}
		if job.IsFinished(res.Status) {
			return res, nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return job.JobResult{}, fmt.Errorf("job %s did not finish within the wait window", id)
		}
		time.Sleep(300 * time.Millisecond)
	}
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
	// bd h-aii-s9ck：生效 deadline 与截断提示，解释 job 为何在请求时长之前被杀
	// （0 = 无 deadline，例如未显式给 timeout 的交互会话）。
	c.Printf("timeout:    %ds\n", res.TimeoutSec)
	if res.TimeoutClamped {
		c.Printf("            (requested %ds, clamped to the project ceiling)\n", res.RequestedTimeoutSec)
	}
	// RECOV-01：worker 断线后台 job 被 held 在 recovering（同实例重连→running，窗口超时→
	// failed/worker_lost）。打印进入 recovering 的时刻（本地时间），与 list 的 STARTED 同格式。
	if res.RecoveringSince > 0 {
		c.Printf("recovering_since: %s\n", formatStarted(res.RecoveringSince))
	}
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
	// bd h-aii-0ql3：只读 job（沙箱）——回答"这次运行是否被允许写文件"。
	if res.ReadOnly {
		c.Printf("read_only:  true\n")
	}
	// JOB-11：同 cwd 独占/共享 + 等在目录锁上时的持有者——回答"为什么我的 job 还没跑"。
	c.Printf("dir:        %s\n", dirLockLabel(res.DirExclusive))
	if res.Status == job.StatusWaitingDir {
		c.Printf("waiting_dir: holder=%s\n", res.WaitingOnJob)
	}
	// GATE-01 S3：人工验收——是否要求人验收，以及已经做出的裁决（谁/何时/为什么）。
	// needs_review 时 reviewed_* 为空，正说明"还没人裁"。
	if res.RequireReview {
		c.Printf("require_review: true\n")
	}
	if res.ReviewedBy != "" {
		c.Printf("reviewed_by: %s\n", res.ReviewedBy)
	}
	if res.ReviewedAt > 0 {
		c.Printf("reviewed_at: %s\n", formatStarted(res.ReviewedAt))
	}
	if res.ReviewNote != "" {
		c.Printf("review_note: %s\n", res.ReviewNote)
	}
	// WT-01：受管 worktree 的交付物位置与分支状态（commits_ahead>0 = 分支上已提交、
	// 还没合回基线分支的交付物；这就是"job 干完了但代码还没合"的可视信号）。
	if res.WorktreePath != "" {
		c.Printf("worktree:   %s\n", res.WorktreePath)
		c.Printf("wt_branch:  %s\n", res.WorktreeBranch)
		if res.WorktreeBaseSHA != "" {
			c.Printf("wt_base:    %s\n", res.WorktreeBaseSHA)
		}
		if res.WorktreeHeadSHA != "" {
			c.Printf("wt_head:    %s (%d commit(s) ahead)\n", res.WorktreeHeadSHA, res.CommitsAhead)
		}
	}
	// SUP-01 C：checklist 挂接 + 提交列表——"这个 job 为哪个 todo 跑、从哪个提交开始、
	// 交付了哪些提交"。
	if res.TodoID != "" {
		c.Printf("todo:       %s\n", res.TodoID)
	}
	if res.BaseSHA != "" {
		c.Printf("base_sha:   %s\n", res.BaseSHA)
	}
	if len(res.Commits) > 0 {
		c.Printf("commits:    %d\n", len(res.Commits))
		for _, cm := range res.Commits {
			c.Printf("            %s %s\n", cm.SHA, cm.Subject)
		}
	}
	// SUP-01 P2：验证步骤的结果——"验收跑了什么、过没通过、为什么"。skipped 说明 agent 没
	// 正常结束（reason 写明是 agent 失败/取消/超时），failed/timeout 就是 job 失败的原因。
	if v := res.Verify; v != nil {
		c.Printf("verify:     %s\n", formatVerify(v))
	}
	// SUP-01 E：该 job 的 token/成本结算（agent 自报，来源在行尾）。没采集到（agent 不报 /
	// 采集失败）就不打印——不伪造 0 用量。
	if line := job.FormatUsage(res.Usage); line != "" {
		c.Printf("usage:      %s\n", line)
	}
	// XFER-01 X2：随 job 传的文件——上传放在哪、收回了多少、跳过了什么。没带文件的 job
	// 不打印（不伪造一行空的传输）。
	if line := formatXfer(res.Xfer); line != "" {
		c.Printf("xfer:       %s\n", line)
	}
	if res.Error != "" {
		c.Printf("error:      %s\n", res.Error)
	}
	return nil
}

// defaultLogWindowLines is the line count `job logs --head/--tail` uses without -n.
const defaultLogWindowLines = 20

// resolveLogOpts maps the `job logs` flags onto a log window request. Without
// -n/--head/--tail the result keeps the legacy byte-tail read (Lines == 0); -n
// alone means the last N lines; --head/--tail without -n default to
// defaultLogWindowLines.
func resolveLogOpts(stream string, stderr bool, lines int, head, tail bool) (client.LogOpts, error) {
	if head && tail {
		return client.LogOpts{}, fmt.Errorf("--head and --tail are mutually exclusive")
	}
	if stderr && stream != "" && stream != "stderr" {
		return client.LogOpts{}, fmt.Errorf("--stderr conflicts with --stream %s", stream)
	}
	if lines < 0 {
		return client.LogOpts{}, fmt.Errorf("--lines must be >= 1")
	}
	if stderr {
		stream = "stderr"
	}
	if stream == "" {
		stream = "stdout"
	}
	if (head || tail) && lines == 0 {
		lines = defaultLogWindowLines
	}
	return client.LogOpts{Stream: stream, Lines: lines, Head: head}, nil
}

func runJobLogs(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("job logs requires an <id> argument")
	}
	opts, err := resolveLogOpts(jobCommonOpts.logsStream, jobCommonOpts.logsStderr,
		jobCommonOpts.logsLines, jobCommonOpts.logsHead, jobCommonOpts.logsTail)
	if err != nil {
		return err
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	out, err := cli.GetLogsWindow(id, opts)
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

// jobAcceptOpts / jobRejectOpts hold the review subcommand flags (GATE-01 S3). The
// note is optional on accept and required on reject.
var (
	jobAcceptOpts struct{ note string }
	jobRejectOpts struct {
		note   string
		resume bool
	}
)

// jobReviewOpts holds `job review` flags (REV-01 §1.3): how much of the agent's
// final report to print, and whether to append the full diff.
var jobReviewOpts = struct {
	tail int
	diff bool
}{}

// defaultReviewTailLines is how much of the agent's report `job review` shows
// without --tail: a summary plus its closing lines, still on one screen next to the
// other four blocks.
const defaultReviewTailLines = 60

// reviewLogBytes is the stdout window `job review` fetches before trimming to lines
// (design §1.3: one bounded read, then take the last N lines client-side — the
// server's line-window endpoint would be a second contract to keep in step).
const reviewLogBytes = 64 * 1024

// runJobReview prints a job's验收材料 on one screen (REV-01 §1.3): what the job
// became (status/review), what it proved (verify), what it delivered (commits/diff
// stat), what it cost (usage) and what the agent REPORTED (the tail of stdout).
// Nothing here is new information — it is the same five surfaces `job show`, `job
// logs`, `job diff` and the web panel read, assembled for the person doing the
// accepting inside the container. Viewing only: the exit code stays 0 whatever the
// job's status (accept/reject remain separate commands).
func runJobReview(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("job review requires an <id> argument")
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
	if res.Title != "" {
		c.Printf("title:      %s\n", res.Title)
	}
	c.Printf("project:    %s\nagent:      %s\nrunner:     %s\n", res.ProjectKey, res.Agent, res.Runner)
	c.Printf("status:     %s\n", res.Status)
	c.Printf("exit_code:  %d\n", res.ExitCode)
	// 验收裁决 (GATE-01 S3)：谁/何时/为什么。缺省（还没人裁）只说等待，不伪造一行空字段。
	if res.RequireReview {
		c.Printf("require_review: true\n")
	}
	if res.ReviewedBy != "" {
		c.Printf("reviewed_by: %s\nreviewed_at: %s\n", res.ReviewedBy, formatStarted(res.ReviewedAt))
		if res.ReviewNote != "" {
			c.Printf("review_note: %s\n", res.ReviewNote)
		}
	} else if res.Status == job.StatusNeedsReview {
		c.Printf("review:     (awaiting a human: `job accept %s` or `job reject %s --note …`)\n", res.ID, res.ID)
	}
	// 提交 / checklist 挂接：交付物落在哪个基线上、为哪个 todo 跑。
	if res.TodoID != "" {
		c.Printf("todo:       %s\n", res.TodoID)
	}
	if res.BaseSHA != "" {
		c.Printf("base_sha:   %s\n", res.BaseSHA)
	}
	if len(res.Commits) > 0 {
		commits := res.Commits
		c.Printf("commits:    %d\n", len(commits))
		if len(commits) > reviewMaxCommits {
			commits = commits[:reviewMaxCommits]
		}
		for _, cm := range commits {
			c.Printf("            %s %s\n", cm.SHA, cm.Subject)
		}
		if len(res.Commits) > reviewMaxCommits {
			c.Printf("            … %d more\n", len(res.Commits)-reviewMaxCommits)
		}
	}
	if v := res.Verify; v != nil {
		c.Printf("verify:     %s\n", formatVerify(v))
	}
	if line := job.FormatUsage(res.Usage); line != "" {
		c.Printf("usage:      %s\n", line)
	}
	if res.Error != "" {
		c.Printf("error:      %s\n", res.Error)
	}
	// diff 摘要：job 行上的 --stat；没采集到（非 git 仓 / 采集失败）就说明一句，
	// 而不是留一块空白让人以为"没有改动"。
	if strings.TrimSpace(res.DiffSummary) == "" {
		c.Printf("diff --stat: (this job captured no diff)\n")
	} else {
		c.Printf("diff --stat:\n%s\n", strings.TrimRight(res.DiffSummary, "\n"))
	}

	tail := jobReviewOpts.tail
	if tail < 0 {
		tail = 0
	}
	if tail > 0 {
		report, err := cli.GetLogsTail(id, "stdout", reviewLogBytes)
		if err != nil {
			// 日志缺失不是验收材料缺失：其他块照常打印，只说明这一块为什么是空的。
			c.Printf("\nreport (stdout): unavailable (%v)\n", err)
		} else {
			c.Printf("\nreport (stdout, last %d lines):\n%s\n", tail, lastLines(report, tail))
		}
	}

	if jobReviewOpts.diff {
		diff, err := cli.GetJobDiffFull(id)
		if err != nil {
			// Same rule as the report block above: a job that captured no diff (many
			// have nothing uncommitted) is not a failed review — say why the block is
			// empty and keep the exit code 0 the screen contract promises.
			c.Printf("\n--- full diff ---\n(the job captured no diff: %v)\n", err)
		} else {
			c.Printf("\n--- full diff ---\n%s\n", strings.TrimRight(diff, "\n"))
		}
	}
	return nil
}

// reviewMaxCommits caps the commit list `job review` prints: the review screen is a
// summary, and a job that delivered hundreds of commits still answers "what did it
// base on / did it commit at all" in the first twenty.
const reviewMaxCommits = 20

// lastLines returns the last n lines of s, with the trailing newline dropped.
func lastLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// runJobAccept records a human's acceptance of a job awaiting review. The server
// stamps the reviewer from the token, so the CLI never sends an identity.
func runJobAccept(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("job accept requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	res, err := cli.AcceptJob(id, jobAcceptOpts.note)
	if err != nil {
		return err
	}
	c.Printf("job %s accepted: status=%s by=%s\n", res.ID, res.Status, res.ReviewedBy)
	return nil
}

// runJobReject records a human's refusal of a job awaiting review. --note is required
// (the reason, and with --resume the continuation's prompt); --resume starts that
// continuation and prints its id so the caller can watch it.
func runJobReject(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("job reject requires an <id> argument")
	}
	if strings.TrimSpace(jobRejectOpts.note) == "" {
		return fmt.Errorf("job reject requires --note \"<why the delivery is refused>\"")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	res, err := cli.RejectJob(id, jobRejectOpts.note, jobRejectOpts.resume)
	if err != nil {
		return err
	}
	c.Printf("job %s rejected: status=%s by=%s\n", res.ID, res.Status, res.ReviewedBy)
	if res.ResumeJobID != "" {
		c.Printf("continuation job %s started with the note as its prompt\n", res.ResumeJobID)
	}
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
		// bd h-aii-0ql3：只读 job 在 TAGS 列前标 [ro]（列不宜再加一列，标记随行即可见）。
		tags := strings.Join(j.Tags, ",")
		if j.ReadOnly {
			if tags == "" {
				tags = "[ro]"
			} else {
				tags = "[ro] " + tags
			}
		}
		tb.AddRow(j.ID, j.Title, j.Status, j.Channel, j.Client, j.Agent, j.Runner,
			j.ProjectKey, tags, formatStarted(j.StartedAt))
	}
	c.Print(tb.Render())
	return nil
}

// formatVerify renders a job's verify step for `job show`: the status plus the
// detail a reader acts on — the exit code for a failed/timed-out step, the duration
// for one that ran, and the reason for a skipped step (which is WHY there is no
// duration: the agent never finished). SUP-01 P2.
func formatVerify(v *job.VerifyResult) string {
	dur := fmt.Sprintf("%.1fs", float64(v.DurationMs)/1000)
	switch v.Status {
	case job.VerifySkipped:
		if v.Reason != "" {
			return fmt.Sprintf("%s (%s)", v.Status, v.Reason)
		}
		return v.Status
	case job.VerifyPassed:
		return fmt.Sprintf("%s (%s)", v.Status, dur)
	default:
		return fmt.Sprintf("%s (exit %d, %s)", v.Status, v.ExitCode, dur)
	}
}

// formatXfer renders a job's file-transfer summary as one line for `job show`: what
// was uploaded (and whether every upload landed), what was collected and how big it
// was, and how many files were skipped. "" for a job that carried no files.
func formatXfer(x *job.XferSummary) string {
	if x == nil {
		return ""
	}
	var parts []string
	if len(x.Uploads) > 0 {
		ok := 0
		for _, u := range x.Uploads {
			if u.OK {
				ok++
			}
		}
		parts = append(parts, fmt.Sprintf("uploads %d/%d ok", ok, len(x.Uploads)))
	}
	if len(x.Collected) > 0 {
		var bytes int64
		for _, f := range x.Collected {
			bytes += f.Size
		}
		parts = append(parts, fmt.Sprintf("collected %d (%s)", len(x.Collected), humanBytes(bytes)))
	}
	if len(x.Skipped) > 0 {
		parts = append(parts, fmt.Sprintf("skipped %d", len(x.Skipped)))
	}
	return strings.Join(parts, " · ")
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
