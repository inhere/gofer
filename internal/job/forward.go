package job

import (
	"github.com/inhere/gofer/internal/runner"
)

// remoteForward builds the forwarding request a worker runner dispatches for a job: the
// ORIGINAL request, as the executing machine needs it. It is Submit's literal moved here
// unchanged so the re-dispatch of a restarted worker session (session_redispatch.go) builds
// its frame from the same single definition.
func remoteForward(req JobRequest, prompt string, timeoutSec int, runReq runner.Request) *runner.Forward {
	return &runner.Forward{
		ProjectKey: req.ProjectKey,
		Agent:      req.Agent,
		PeerRunner: builtinLocalRunner,
		// Prompt is what the executing machine RUNS with. It equals req.Prompt for
		// every remote job (the list is rendered by the executing machine — JOB-10
		// 决策 1), and prompt is threaded through anyway so a future local forward
		// cannot silently drop the mount.
		Prompt:    prompt,
		AgentArgs: req.AgentArgs,
		ReadDirs:  req.ReadDirs,
		Model:     req.Model,
		// gofer-f4z8: the executing machine renders ITS agent's from_session_args.
		FromSession: req.FromSession,
		Budget:      req.Budget,
		Cmd:         req.Cmd,
		Cwd:         req.Cwd,
		// WT-01: the executor creates the worktree (that machine owns the
		// checkout); the submitting side already resolved worktree_default into
		// req.Worktree.
		Worktree:     req.Worktree,
		WorktreeBase: req.WorktreeBase,
		// 2026-10-21 design 一: the name/branch overrides travel with the job so the
		// executor renders the same layout from ITS project templates.
		WorktreeName:   req.WorktreeName,
		WorktreeBranch: req.WorktreeBranch,
		// `ref:` (design 一·3): the executor checks out this commit in a temporary
		// worktree, fetching it first when its own checkout lacks it.
		Ref: req.Ref,
		// The job's extra env (a workflow step / fan env) rides Dispatch.Env; the
		// worker layers it onto its own gofer metadata env.
		Env: req.Env,
		// The ADMITTED deadline (clamped above), not the raw request: the server
		// owns admission, so a remote execution must not re-admit 2h for a request
		// this server already cut to 1h (bd h-aii-s9ck).
		TimeoutSec:     timeoutSec,
		Session:        req.Session,
		IdleTimeoutSec: req.IdleTimeoutSec,
		MaxSessionSec:  req.MaxSessionSec,
		Messenger:      req.Messenger,
		Interactive:    req.Interactive,
		Cols:           req.Cols,
		Rows:           req.Rows,
		// Path B priming rides to the executor, whose pty starts the resumed
		// TUI: the text and the quiet window travel together (see above).
		InitialInput:        runReq.InitialInput,
		InitialInputQuietMs: runReq.InitialInputQuietMs,
		Stdin:               req.Stdin,
		ResumeSourceAgent:   req.ResumeSourceAgent,
		SystemPrompt:        req.SystemPrompt,
		// ACP-01 S2: a continuation reaches a remote executor with its session and
		// lineage so the worker's local job resolves the same session/load. Empty for
		// a plain job (a bare session_id is not a resume).
		SessionID:         req.SessionID,
		ResumedFrom:       req.ResumedFrom,
		AutoResumeAttempt: req.AutoResumeAttempt,
		// bd h-aii-0ql3: read-only rides to the executor, whose own admission +
		// agent config decide how (or whether) it can be honoured.
		ReadOnly: req.ReadOnly,
		// The tier rides to the executor, whose approval gate it tightens.
		PermissionTier: req.PermissionTier,
		// Hosted rides to the executor: its own Submit is what keeps the idle default off.
		Hosted: req.Hosted,
		// CFG-16: `--no-rules` also turns off the tools section the EXECUTING
		// machine renders, so the switch travels with the job.
		NoRules: req.NoRules,
		// P2: the resolved target worker (explicit req.WorkerID or label-selected
		// in selectTargetWorker). Empty for peer-http and for worker jobs relying
		// on the runner's configured default (D4).
		WorkerID: req.WorkerID,
		// SUP-01 P2: the resolved verify step (argv + its own deadline) rides to
		// the executor, which owns the checkout being verified. The hub refuses a
		// dispatch to a worker that cannot honour it (protocol < 8).
		Verify:           req.Verify,
		VerifyTimeoutSec: req.VerifyTimeoutSec,
		// XFER-01 X2: the file steps run on the EXECUTING machine (it owns the cwd
		// they act on), so the staged upload ids and the collect globs travel with
		// the dispatch. Empty for a job that carries no files — byte-identical to a
		// pre-X2 forward.
		Uploads: xferUploadsToRunner(req.Uploads),
		Collect: req.Collect,
		// JOB-10: the resolved skills travel with the dispatch so the worker mounts
		// exactly what this machine decided (the files themselves ride the uploads
		// above, with Base=result_dir).
		Skills: req.Skills,
		// MCP-06: the bound MCP servers' declarations (secret variable NAMES only).
		MCP: mcpWireSpecs(req.MCP, req.MCPSpecs),
		// JOB-11: the resolved same-directory lock decision travels to the machine
		// that owns the checkout — its job.Service is the one that can actually hold
		// the lock (this process only knows a relative cwd for a remote job).
		ExclusiveDir: req.ExclusiveDir,
		// AUTO-05: so does the output-stall window: the executor owns the process
		// whose silence is being watched.
		StallTimeoutSec: req.StallTimeoutSec,
		// JOB-11 (F12): and so does the directory-lock wait cap — the lock is held
		// on that machine, so the policy this hub resolved is what applies there.
		DirWaitMaxSec: req.DirWaitMaxSec,
		KeepDirLock:   req.KeepDirLock,
		EnvDenyExtra:  req.EnvDenyExtra,
	}
}
