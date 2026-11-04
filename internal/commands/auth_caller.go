package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/jobstore"
)

// auth_caller.go implements `gofer auth caller`: 用户管理（配置入库 P1，计划 T7）。它和别的
// `auth` 子命令一样是瘦客户端（G033：放在已有的 `auth` 下，不新增顶级命令）——token 由 server
// 生成、用户行由 server 写、生效也在 server 侧，CLI 只发请求、打印一次 token 与结果。
//
// 唯一不经过 server 的入口是离线修复 `auth caller repair <id> --offline`（D5）：server 起不来
// 时，坏的用户行（手改库、版本变化）要用它修到能启动。它只往更严的方向改，且不建库。
//
//	gofer auth caller add ci --ceiling-project p1      — 新建用户，上限只能碰项目 p1
//	gofer auth caller add ci --no-ceiling              — 明确不设上限
//	gofer auth caller ls                               — 来源、状态、token 提示、上限与配额
//	gofer auth caller rotate ci                        — 换 token（旧 token 立刻失效）
//	gofer auth caller disable ci / enable ci           — 停用 / 启用（id 不复用）
//	gofer auth caller set-ceiling ci --no-ceiling      — 改上限
//	gofer auth caller set-limits ci --rate-limit 1     — 改配额
//	gofer auth caller audit                            — 谁改了什么（打码后的前后值）
//	gofer auth caller repair ci --offline              — 坏行修到能启动（server 未运行时）
var authCallerOpts struct {
	name     string
	maxConc  int
	rateLim  float64
	rateBrst int

	// 上限：可重复的 glob 列表 + 三个开关。都不给且没有 --no-ceiling 就是错误（D16：新建
	// 用户必须明确上限）。
	ceilingProjects gcli.Strings
	ceilingRunners  gcli.Strings
	ceilingWorkers  gcli.Strings
	ceilingAgents   gcli.Strings
	allowExec       bool
	globalMemory    bool
	readOnly        bool
	noCeiling       bool

	auditLimit int

	offline bool
	force   bool
	asJSON  bool
}

// newAuthCallerCmd builds the `auth caller` group.
func newAuthCallerCmd() *gcli.Command {
	add := &gcli.Command{
		Name: "add",
		Desc: "Create a caller and print its token once (the token is never shown again)",
		Help: `Creates a caller of this deployment and prints the token ONCE: the server keeps only its
sha256, so a lost token has to be rotated, not recovered. The caller works on the next
request — no reload, no restart, and the configuration file is not touched.

A ceiling is REQUIRED, in one of its two forms:

  --ceiling-project / --ceiling-runner / --ceiling-worker / --ceiling-agent <glob>  (repeatable)
  --ceiling-allow-exec / --ceiling-global-memory / --ceiling-read-only
      list what the caller may reach; every dimension left out is not restricted, EXCEPT
      runners: with no --ceiling-runner the caller may NOT use the machine runner (local),
      only remote workers. A job on the machine runner runs on the server host and can read
      its configuration and every token, so a ceiling that allows it is not a security boundary.
  --no-ceiling
      say on purpose that the caller is unlimited. The answer warns what such a caller can do.

The id is never reused: an existing caller (enabled, disabled or taken over), a caller of the
configuration file and a worker binding all refuse the same id.

  gofer auth caller add ci --ceiling-project p1 --ceiling-agent 'claude*'
  gofer auth caller add bot --no-ceiling --rate-limit 2 --max-concurrent-jobs 2`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("id", "caller id (^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$; not default / system / root, no job- prefix)", true)
			c.StrOpt(&authCallerOpts.name, "name", "", "", "display name (optional; the id stays the identity)")
			c.IntOpt(&authCallerOpts.maxConc, "max-concurrent-jobs", "", 0, "how many jobs this caller may run at once (0 = take the server.governance default)")
			c.Float64Opt(&authCallerOpts.rateLim, "rate-limit", "", 0, "submits per second (0 = take the server.governance default)")
			c.IntOpt(&authCallerOpts.rateBrst, "rate-burst", "", 0, "burst capacity of the submit rate (0 = derived from --rate-limit)")
			bindCallerCeilingFlags(c)
			c.BoolOpt(&authCallerOpts.noCeiling, "no-ceiling", "", false, "do not limit this caller (the alternative to a --ceiling-* block)")
		},
		Func: runAuthCallerAdd,
	}
	ls := &gcli.Command{
		Name: "ls",
		Desc: "List the callers: source, state, token hint, ceiling and quota",
		Help: `Lists every caller this deployment knows: the ones declared in the configuration file and
the ones stored in the database, including the disabled and the taken-over ones. The token is
never shown — only ` + "`token_hint`" + `, which is enough to tell two tokens apart.

  gofer auth caller ls
  gofer auth caller ls --json`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.BoolOpt(&authCallerOpts.asJSON, "json", "", false, "print the server's answer as JSON")
		},
		Func: runAuthCallerList,
	}
	rotate := &gcli.Command{
		Name: "rotate",
		Desc: "Replace a caller's token; the old one stops working at once",
		Help: `Replaces the caller's token and prints the new one ONCE. The old token stops working at
once (an open long connection of that caller ends), and the caller keeps its state: rotating a
disabled caller does not enable it.

A caller declared in the configuration file cannot be rotated here: its token would have to be
written into the file. Change (or remove) that entry instead.

  gofer auth caller rotate ci`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("id", "caller id", true)
		},
		Func: runAuthCallerRotate,
	}
	disable := &gcli.Command{
		Name: "disable",
		Desc: "Switch a caller off: it authenticates nothing and starts no new job",
		Help: `Switches a caller off. From the next request on its token authenticates nothing, every
permission of it is empty, and no new job of its owner starts (queued jobs fail instead of
running). The ceiling of the entry keeps bounding the jobs it already started, and the caller
stays listed so it can be enabled again.

A caller declared in the configuration file cannot be disabled here: edit the file.

  gofer auth caller disable ci`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("id", "caller id", true)
		},
		Func: runAuthCallerDisable,
	}
	enable := &gcli.Command{
		Name: "enable",
		Desc: "Bring a disabled caller back",
		Help: `Brings a disabled caller back: the same token works again (disabling is not revoking).

A caller a configuration-file entry TOOK OVER (same id) can only come back after a rotation:
otherwise the token from before the takeover would work again.

  gofer auth caller enable ci`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("id", "caller id", true)
		},
		Func: runAuthCallerEnable,
	}
	setCeiling := &gcli.Command{
		Name: "set-ceiling",
		Desc: "Replace a caller's ceiling (or remove it with --no-ceiling)",
		Help: `Replaces what the caller may reach. The whole ceiling is replaced, so a dimension left out is
not restricted — EXCEPT runners: with no --ceiling-runner the caller may NOT use the machine
runner (local), only remote workers.

A caller declared in the configuration file has its ceiling written to that file (the server
re-renders the ` + "`server:`" + ` block, so comments there are lost, like every other server-field
write); a stored caller's ceiling is written to the database and applies at once.

  gofer auth caller set-ceiling ci --ceiling-project p1 --ceiling-project p2
  gofer auth caller set-ceiling ci --no-ceiling`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("id", "caller id", true)
			bindCallerCeilingFlags(c)
			c.BoolOpt(&authCallerOpts.noCeiling, "no-ceiling", "", false, "remove the ceiling: the caller becomes unlimited")
		},
		Func: runAuthCallerSetCeiling,
	}
	setLimits := &gcli.Command{
		Name: "set-limits",
		Desc: "Replace a caller's quota (concurrency / submit rate)",
		Help: `Replaces the caller's quota overrides. A value left out means "take the server.governance
default", so setting only --rate-limit clears the other two. A raised concurrency limit applies
to the next job without a restart.

  gofer auth caller set-limits ci --max-concurrent-jobs 4 --rate-limit 2`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("id", "caller id", true)
			c.IntOpt(&authCallerOpts.maxConc, "max-concurrent-jobs", "", 0, "how many jobs this caller may run at once (0 = server.governance default)")
			c.Float64Opt(&authCallerOpts.rateLim, "rate-limit", "", 0, "submits per second (0 = server.governance default)")
			c.IntOpt(&authCallerOpts.rateBrst, "rate-burst", "", 0, "burst capacity of the submit rate (0 = derived from --rate-limit)")
		},
		Func: runAuthCallerSetLimits,
	}
	audit := &gcli.Command{
		Name: "audit",
		Desc: "Show who changed which caller, when, with the masked values",
		Help: `Prints the audit lines of every caller change, newest first: who did it, when, which entry,
and the masked before / after values (name, quota, ceiling, state, token hint — never a token
and never its hash).

  gofer auth caller audit
  gofer auth caller audit --limit 20`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.IntOpt(&authCallerOpts.auditLimit, "limit", "", 0, "how many lines to show (default 100, at most 500)")
		},
		Func: runAuthCallerAudit,
	}
	repair := &gcli.Command{
		Name: "repair",
		Desc: "Make a broken stored caller startable again (offline, server stopped)",
		Help: `Repairs ONE stored caller while the server is NOT running, so a server that refuses to start
because of that row starts again. It only ever makes the caller more restrictive and never
deletes the row or touches the token hash:

  - an unusable ceiling becomes the most restrictive one (nothing matches, read-only);
  - an unusable quota is cleared;
  - an unusable display name is cleared;
  - a row repaired this way is disabled (rotate it and enable it once its fields are usable).

It opens the database the configuration points at READ-ONLY in the sense of "existing only": a
wrong path is an error instead of a new empty database, and the path is printed so you can see
which database was repaired. The pid file of a daemon-mode serve is only a hint: stop the
server first, so the repair cannot race a running one (` + "`--force`" + ` repairs anyway, with a warning).

  gofer auth caller repair ci --offline
  gofer auth caller repair ci --offline --config /etc/gofer/config.yaml`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			c.AddArg("id", "caller id to repair", true)
			c.BoolOpt(&authCallerOpts.offline, "offline", "", false, "required: repair a stopped server's database")
			c.BoolOpt(&authCallerOpts.force, "force", "", false, "repair even when the pid file names a running serve (only a warning)")
		},
		Func: runAuthCallerRepair,
	}
	group := &gcli.Command{
		Name: "caller",
		Desc: "Manage callers: add, list, rotate, disable / enable, change the ceiling or the quota",
		Subs: []*gcli.Command{add, ls, rotate, disable, enable, setCeiling, setLimits, audit, repair},
	}
	return group
}

// bindCallerCeilingFlags binds the ceiling block's flags (shared by add and set-ceiling).
func bindCallerCeilingFlags(c *gcli.Command) {
	c.VarOpt(&authCallerOpts.ceilingProjects, "ceiling-project", "", "project key the caller may use (glob, repeatable)")
	c.VarOpt(&authCallerOpts.ceilingRunners, "ceiling-runner", "", "runner key the caller may submit to (glob, repeatable). Without it the caller may NOT use the machine runner (local), only remote workers")
	c.VarOpt(&authCallerOpts.ceilingWorkers, "ceiling-worker", "", "worker the caller's jobs may be routed to (glob, repeatable)")
	c.VarOpt(&authCallerOpts.ceilingAgents, "ceiling-agent", "", "agent key the caller may use (glob, repeatable)")
	c.BoolOpt(&authCallerOpts.allowExec, "ceiling-allow-exec", "", false, "allow exec jobs (agent exec, a Cmd, a Verify argv, hooks)")
	c.BoolOpt(&authCallerOpts.globalMemory, "ceiling-global-memory", "", false, "allow writing memory that is not scoped to one project")
	c.BoolOpt(&authCallerOpts.readOnly, "ceiling-read-only", "", false, "refuse every state-changing request of this caller")
}

// callerCeilingFromFlags builds the ceiling the flags describe, plus whether any of them was
// given (so "neither a ceiling nor --no-ceiling" can be an error, D16).
func callerCeilingFromFlags() (*client.CallerCeiling, bool) {
	ceil := &client.CallerCeiling{
		Projects: []string(authCallerOpts.ceilingProjects),
		Runners:  []string(authCallerOpts.ceilingRunners),
		Workers:  []string(authCallerOpts.ceilingWorkers),
		Agents:   []string(authCallerOpts.ceilingAgents),
	}
	given := len(ceil.Projects)+len(ceil.Runners)+len(ceil.Workers)+len(ceil.Agents) > 0 ||
		authCallerOpts.allowExec || authCallerOpts.globalMemory || authCallerOpts.readOnly
	if authCallerOpts.allowExec {
		ceil.AllowExec = callerBoolPtr(true)
	}
	if authCallerOpts.globalMemory {
		ceil.GlobalMemory = callerBoolPtr(true)
	}
	if authCallerOpts.readOnly {
		ceil.ReadOnly = callerBoolPtr(true)
	}
	return ceil, given
}

// callerCeilingForSend returns the ceiling to send, or the reason there is none to send: an
// empty one is refused locally, because the server refuses it too (D16) and a local message
// names the flags.
func callerCeilingForSend() (*client.CallerCeiling, error) {
	ceil, given := callerCeilingFromFlags()
	if authCallerOpts.noCeiling {
		if given {
			return nil, fmt.Errorf("either give a --ceiling-* block or --no-ceiling, not both")
		}
		return nil, nil
	}
	if !given {
		return nil, fmt.Errorf("a ceiling is required: give --ceiling-project / --ceiling-runner / " +
			"--ceiling-worker / --ceiling-agent / --ceiling-allow-exec / --ceiling-global-memory / " +
			"--ceiling-read-only, or --no-ceiling to leave the caller unlimited")
	}
	return ceil, nil
}

// callerBoolPtr is the small helper the wire form of a ceiling needs (an unset switch and a
// false one are different values).
func callerBoolPtr(v bool) *bool { return &v }

// runAuthCallerAdd runs `gofer auth caller add`.
func runAuthCallerAdd(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return authFail("%v", err)
	}
	return runCallerAdd(c, cli)
}

// runCallerAdd is the test seam (a stub server drives it).
func runCallerAdd(c *gcli.Command, cli *client.Client) error {
	id := strings.TrimSpace(c.Arg("id").String())
	if id == "" {
		return authFail("give the caller id: gofer auth caller add <id> --ceiling-project <p>")
	}
	ceil, err := callerCeilingForSend()
	if err != nil {
		return authFail("%v", err)
	}
	change, err := cli.AddCaller(client.CallerAddRequest{
		ID: id, Name: authCallerOpts.name,
		MaxConcurrentJobs: authCallerOpts.maxConc, RateLimit: authCallerOpts.rateLim, RateBurst: authCallerOpts.rateBrst,
		Ceiling: ceil, NoCeiling: authCallerOpts.noCeiling,
	})
	if err != nil {
		return authFail("%v", err)
	}
	if authCallerOpts.asJSON {
		return printTrackerJSON(c, change)
	}
	printCallerChange(c, change, true)
	return nil
}

// runAuthCallerList runs `gofer auth caller ls`.
func runAuthCallerList(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return authFail("%v", err)
	}
	return runCallerList(c, cli)
}

// runCallerList is the test seam.
func runCallerList(c *gcli.Command, cli *client.Client) error {
	callers, err := cli.ListCallers()
	if err != nil {
		return authFail("%v", err)
	}
	if authCallerOpts.asJSON {
		return printTrackerJSON(c, map[string]any{"callers": callers})
	}
	if len(callers) == 0 {
		c.Println("no caller")
		return nil
	}
	width := len("ID")
	for _, cl := range callers {
		if len(cl.ID) > width {
			width = len(cl.ID)
		}
	}
	c.Printf("%-*s  %-55s  %-10s  %-14s  %s\n", width, "ID", "NAME", "SOURCE", "STATE", "CEILING / QUOTA")
	for _, cl := range callers {
		c.Printf("%-*s  %-55s  %-10s  %-14s  %s\n", width, cl.ID, callerNameCell(cl), cl.Source, cl.State,
			callerLimitCell(cl))
	}
	return nil
}

// callerNameCell renders the name (or the token hint when the name is empty), so a list row
// always says which credential it is about.
func callerNameCell(cl client.Caller) string {
	if strings.TrimSpace(cl.Name) != "" {
		return cl.Name
	}
	if cl.TokenHint != "" {
		return cl.TokenHint
	}
	return "-"
}

// callerLimitCell summarizes the ceiling and the quota of one caller.
func callerLimitCell(cl client.Caller) string {
	var parts []string
	if cl.Ceiling == nil {
		parts = append(parts, "no ceiling")
	} else {
		list := func(label string, v []string, empty string) {
			if len(v) == 0 {
				parts = append(parts, label+": "+empty)
				return
			}
			parts = append(parts, label+": "+strings.Join(v, ","))
		}
		list("projects", cl.Ceiling.Projects, "any")
		list("runners", cl.Ceiling.Runners, "workers only")
		parts = append(parts, "exec: "+yesNoLabel(cl.Ceiling.AllowExec != nil && *cl.Ceiling.AllowExec))
		if cl.Ceiling.ReadOnly != nil && *cl.Ceiling.ReadOnly {
			parts = append(parts, "read-only")
		}
	}
	if cl.MaxConcurrentJobs > 0 {
		parts = append(parts, fmt.Sprintf("max_jobs: %d", cl.MaxConcurrentJobs))
	}
	if cl.RateLimit > 0 {
		parts = append(parts, fmt.Sprintf("rate: %g/s", cl.RateLimit))
	}
	return strings.Join(parts, "; ")
}

// runAuthCallerRotate runs `gofer auth caller rotate`.
func runAuthCallerRotate(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return authFail("%v", err)
	}
	return runCallerRotate(c, cli)
}

// runCallerRotate is the test seam.
func runCallerRotate(c *gcli.Command, cli *client.Client) error {
	id := strings.TrimSpace(c.Arg("id").String())
	if id == "" {
		return authFail("give the caller id to rotate: gofer auth caller rotate <id>")
	}
	change, err := cli.RotateCaller(id)
	if err != nil {
		return authFail("%v", err)
	}
	if authCallerOpts.asJSON {
		return printTrackerJSON(c, change)
	}
	printCallerChange(c, change, true)
	return nil
}

// runAuthCallerDisable runs `gofer auth caller disable`.
func runAuthCallerDisable(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return authFail("%v", err)
	}
	return runCallerState(c, cli, "disable")
}

// runAuthCallerEnable runs `gofer auth caller enable`.
func runAuthCallerEnable(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return authFail("%v", err)
	}
	return runCallerState(c, cli, "enable")
}

// runCallerState is the test seam of the two state commands.
func runCallerState(c *gcli.Command, cli *client.Client, action string) error {
	id := strings.TrimSpace(c.Arg("id").String())
	if id == "" {
		return authFail("give the caller id: gofer auth caller %s <id>", action)
	}
	var (
		change client.CallerChange
		err    error
	)
	if action == "disable" {
		change, err = cli.DisableCaller(id)
	} else {
		change, err = cli.EnableCaller(id)
	}
	if err != nil {
		return authFail("%v", err)
	}
	if authCallerOpts.asJSON {
		return printTrackerJSON(c, change)
	}
	printCallerChange(c, change, false)
	return nil
}

// runAuthCallerSetCeiling runs `gofer auth caller set-ceiling`.
func runAuthCallerSetCeiling(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return authFail("%v", err)
	}
	return runCallerSetCeiling(c, cli)
}

// runCallerSetCeiling is the test seam.
func runCallerSetCeiling(c *gcli.Command, cli *client.Client) error {
	id := strings.TrimSpace(c.Arg("id").String())
	if id == "" {
		return authFail("give the caller id: gofer auth caller set-ceiling <id> --ceiling-project <p>")
	}
	ceil, err := callerCeilingForSend()
	if err != nil {
		return authFail("%v", err)
	}
	change, err := cli.SetCallerCeiling(id, client.CallerCeilingRequest{
		Ceiling: ceil, NoCeiling: authCallerOpts.noCeiling,
	})
	if err != nil {
		return authFail("%v", err)
	}
	if authCallerOpts.asJSON {
		return printTrackerJSON(c, change)
	}
	printCallerChange(c, change, false)
	printCallerFileWriteNote(c, change)
	return nil
}

// printCallerFileWriteNote is D12: a file caller's ceiling and limits are written to the
// configuration file, whose `server:` block is re-rendered — the comments in it are lost. A stored
// caller's write touches no file, so it gets no such note.
func printCallerFileWriteNote(c *gcli.Command, change client.CallerChange) {
	if config.CallerSourceLabel(change.Caller.Source) == config.CallerSourceFile {
		c.Println(cliText("cli.auth.caller.file_write_note"))
	}
}

// runAuthCallerSetLimits runs `gofer auth caller set-limits`.
func runAuthCallerSetLimits(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return authFail("%v", err)
	}
	return runCallerSetLimits(c, cli)
}

// runCallerSetLimits is the test seam.
func runCallerSetLimits(c *gcli.Command, cli *client.Client) error {
	id := strings.TrimSpace(c.Arg("id").String())
	if id == "" {
		return authFail("give the caller id: gofer auth caller set-limits <id> --rate-limit 2")
	}
	change, err := cli.SetCallerLimits(id, client.CallerLimitsRequest{
		MaxConcurrentJobs: authCallerOpts.maxConc, RateLimit: authCallerOpts.rateLim, RateBurst: authCallerOpts.rateBrst,
	})
	if err != nil {
		return authFail("%v", err)
	}
	if authCallerOpts.asJSON {
		return printTrackerJSON(c, change)
	}
	printCallerChange(c, change, false)
	printCallerFileWriteNote(c, change)
	return nil
}

// runAuthCallerAudit runs `gofer auth caller audit`.
func runAuthCallerAudit(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return authFail("%v", err)
	}
	return runCallerAudit(c, cli)
}

// runCallerAudit is the test seam.
func runCallerAudit(c *gcli.Command, cli *client.Client) error {
	entries, err := cli.ListCallerAudit(authCallerOpts.auditLimit)
	if err != nil {
		return authFail("%v", err)
	}
	if len(entries) == 0 {
		c.Println("no caller change recorded yet")
		return nil
	}
	for _, e := range entries {
		c.Printf("%s  %-18s %-32s %s\n", callerAuditTime(e.At), e.Actor, e.Path, callerAuditChange(e))
	}
	return nil
}

// callerAuditChange renders the before → after halves of one audit line (already masked by the
// server).
func callerAuditChange(e client.CallerAuditEntry) string {
	before := strings.TrimSpace(e.BeforeMasked)
	if before == "" {
		before = "(new)"
	}
	return before + "  ->  " + strings.TrimSpace(e.AfterMasked)
}

// printCallerChange prints one caller write: the caller, its token when the write minted one,
// and the notices the server answered with.
func printCallerChange(c *gcli.Command, change client.CallerChange, showToken bool) {
	c.Printf("caller:      %s\n", orNone(change.Caller.ID))
	if change.Caller.Name != "" {
		c.Printf("name:        %s\n", change.Caller.Name)
	}
	c.Printf("source:      %s\n", orNone(change.Caller.Source))
	c.Printf("state:       %s\n", orNone(change.Caller.State))
	if change.Caller.TokenHint != "" {
		c.Printf("token hint:  %s\n", change.Caller.TokenHint)
	}
	c.Printf("limits:      %s\n", callerLimitCell(change.Caller))
	if showToken && change.Token != "" {
		c.Printf("\ntoken: %s\n", change.Token)
		c.Println("This is the only time the token is shown: the server keeps just its hash.")
	}
	for _, w := range change.Warnings {
		c.Printf("warning: %s\n", w)
	}
}

// ---- 离线修复（D5）----

// runAuthCallerRepair runs `gofer auth caller repair <id> --offline`. It never reaches the
// server: it repairs the database the configuration points at, after checking that the server
// is not running — the pid file is a best-effort hint (D5), the safety of the repair rests on
// three facts instead: it does not create or migrate a database, it prints which database it
// opened, and it only ever makes the caller more restrictive.
func runAuthCallerRepair(c *gcli.Command, _ []string) error {
	id := strings.TrimSpace(c.Arg("id").String())
	if id == "" {
		return authFail("give the caller id to repair: gofer auth caller repair <id> --offline")
	}
	if !authCallerOpts.offline {
		return authFail("repair works on a stopped server's database only: add --offline " +
			"(the running server owns the database; `gofer auth caller disable` is the online way)")
	}
	if err := callerRepairServerRunning(); err != nil {
		return err
	}
	cfg, _, err := config.Load(config.InputCfgFile)
	if err != nil {
		return authFail("load the configuration: %v", err)
	}
	dbPath, err := cfg.ResolveDBPathChecked()
	if err != nil {
		return authFail("%v", err)
	}
	c.Printf("database:    %s\n", dbPath)
	st, err := jobstore.OpenExistingForRepair(dbPath)
	if err != nil {
		return authFail("%v", err)
	}
	defer func() { _ = st.Close() }()

	res, err := st.RepairCaller(id, "offline:"+trackerActor(), jobstore.RepairOptions{
		ValidCeiling: func(s string) error {
			_, err := config.DecodeCallerCeiling(s)
			return err
		},
		ValidLimits: func(s string) error {
			_, err := config.DecodeCallerLimits(s)
			return err
		},
		ValidName:         config.ValidCallerName,
		LockedCeilingYAML: config.LockedCallerCeilingYAML(),
	})
	if err != nil {
		return authFail("repair caller %s: %v", id, err)
	}
	if !res.Found {
		return authFail("no stored caller %s in %s (a caller declared in the configuration file has nothing to repair)", id, dbPath)
	}
	if len(res.Changed) == 0 {
		c.Printf("caller:      %s (nothing to repair)\n", id)
		return nil
	}
	c.Printf("caller:      %s\n", id)
	c.Printf("repaired:    %s\n", strings.Join(res.Changed, ", "))
	if res.Disabled {
		c.Println("state:       disabled (rotate the token, fix what is listed above, then enable it)")
	}
	for _, field := range res.Changed {
		switch field {
		case "ceiling":
			c.Println("ceiling:     replaced by the most restrictive one (nothing matches; read-only)")
		case "limits":
			c.Println("limits:      cleared (no quota override)")
		case "name":
			c.Println("name:        cleared (display only)")
		}
	}
	c.Println("The repair is recorded in the config audit (actor offline:<you>); the token hash was not touched.")
	return nil
}

// callerRepairServerRunning refuses to repair while a daemon-mode serve holds the pid file.
// --force downgrades it to a warning (D5: the pid file is a hint, not a lock).
func callerRepairServerRunning() error {
	pidPath := servePIDFile()
	pid, err := daemon.ReadPIDFile(pidPath)
	if err != nil {
		return nil // no pid file: nothing to warn about
	}
	if !daemon.PIDAlive(pid) {
		return nil
	}
	if authCallerOpts.force {
		fmt.Fprintf(os.Stderr, "warning: pidfile %s names a live serve (pid=%d); repairing anyway (--force)\n", pidPath, pid)
		return nil
	}
	return authFail("a serve process is running (pidfile %s names pid=%d): stop it first, or pass --force "+
		"to repair an already-broken database anyway", filepath.ToSlash(pidPath), pid)
}

// callerAuditTime renders a unix stamp the way the other CLI views do; the audit lines are
// seconds old at most, so the local zone is the useful one.
func callerAuditTime(ts int64) string {
	if ts == 0 {
		return "-"
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
}
