package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"
	"github.com/gookit/goutil/x/ccolor"

	configtmpl "github.com/inhere/gofer/config"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/worker"
	"github.com/inhere/gofer/skills"
)

// configExitErr is the process exit code returned when `config validate` finds
// a failing check. gcli derives a non-zero exit from errorx.ErrorCoder errors
// (mirrors serveExitErr), so CI can gate on the config being valid.
const configExitErr = 2

// initOpts holds `gofer init` flags.
var initOpts = struct {
	config string
	force  bool
	global bool
}{}

// DefaultInitConfigPath is where `gofer init [server]` writes the starter server
// config when no --config is given: ./.gofer.yaml, the highest-priority
// current-dir candidate in the loader lookup chain (loader.go
// CurrentDirConfigNames), so a freshly generated file is picked up automatically.
const DefaultInitConfigPath = ".gofer.yaml"

// DefaultWorkerConfigPath is where `gofer init worker` writes the starter worker
// config when no --output is given. `gofer worker` reads --worker-config, falling
// back to <config-dir>/worker.yaml when the flag is omitted (loadWorkerConfig).
const DefaultWorkerConfigPath = "worker.yaml"

// initTemplate maps an init target to its embedded starter template + default
// output path. `server` is the default (backward-compatible: bare `gofer init`).
func initTemplate(target string) (tmpl, defaultPath string, ok bool) {
	switch target {
	case "", "server":
		return configtmpl.ExampleYAML, DefaultInitConfigPath, true
	case "worker":
		return configtmpl.WorkerExampleYAML, DefaultWorkerConfigPath, true
	default:
		return "", "", false
	}
}

// NewInitCmd builds the top-level `gofer init [target]` command (E3). target is
// `server` (default), `worker`, or `skill`. For server/worker it writes the
// matching embedded starter to its default path (./.gofer.yaml / ./worker.yaml)
// or --output <path>; for skill it installs the embedded gofer-usage/ tree to
// .claude/skills/. It refuses to overwrite an existing target unless --force is
// given (design D6).
func NewInitCmd() *gcli.Command {
	return &gcli.Command{
		Name: "init",
		Desc: "Scaffold a starter config or skill from the embedded templates (target: server | worker | skill)",
		Config: func(c *gcli.Command) {
			c.AddArg("target", "what to scaffold: server (default) | worker | skill", false)
			c.StrOpt(&initOpts.config, "output", "o", "", "output path (config file for server/worker; skills parent dir for skill)")
			c.BoolOpt(&initOpts.force, "force", "f", false, "overwrite an existing config file or skill")
			c.BoolOpt(&initOpts.global, "global", "g", false, "write to the user-global dir (<config-dir>/config.yaml|worker.yaml for server/worker; ~/.claude/skills for skill)")
		},
		Func: runInit,
	}
}

// runInit writes the embedded example template for the chosen target to the
// output path. The templates are the single source of truth shared with
// config/{gofer,worker}.example.yaml (no drift).
func runInit(c *gcli.Command, _ []string) error {
	target := "server"
	if a := c.Arg("target"); a != nil && a.String() != "" {
		target = strings.ToLower(a.String())
	}
	// skill installs a directory tree (not a single templated file), so it takes a
	// separate path — initTemplate stays config-only (server|worker).
	if target == "skill" {
		return runInitSkill(c)
	}
	tmpl, defaultPath, ok := initTemplate(target)
	if !ok {
		return errorx.Failf(configExitErr, "unknown init target %q (use: server | worker | skill)", target)
	}

	// Path resolution: an explicit --output always wins (backward compatible).
	// Otherwise --global writes to the user-global config dir for BOTH targets —
	// config.yaml for server, worker.yaml for worker (the very path `gofer worker`
	// auto-discovers via UserWorkerConfigPath). Without --global, the CWD default.
	path := initOpts.config
	usedGlobal := false
	if path == "" {
		if initOpts.global {
			gp, err := globalConfigPath(target)
			if err != nil {
				return errorx.Failf(configExitErr, "resolve global config path: %v", err)
			}
			path = gp
			usedGlobal = true
			_ = os.MkdirAll(filepath.Dir(path), 0o755) // ensure the config dir exists
		} else {
			path = defaultPath
		}
	}

	if !initOpts.force {
		if _, err := os.Stat(path); err == nil {
			// Refuse to clobber a real config (design D6); coded error → non-zero exit.
			return errorx.Failf(configExitErr, "config %s already exists; use --force to overwrite", path)
		} else if !os.IsNotExist(err) {
			return errorx.Failf(configExitErr, "stat %s: %v", path, err)
		}
	}

	if err := os.WriteFile(path, []byte(tmpl), 0o644); err != nil {
		return errorx.Failf(configExitErr, "write %s: %v", path, err)
	}
	if target == "worker" {
		if usedGlobal {
			// Global worker.yaml is the path `gofer worker` auto-discovers, so no
			// --worker-config is needed.
			c.Printf("已生成全局 worker 配置 %s，编辑后直接运行 `gofer worker` 即可(自动发现该路径)\n", path)
		} else {
			c.Printf("已生成 worker 配置 %s，编辑后运行 `gofer worker --worker-config %s` 启动\n", path, path)
		}
	} else {
		c.Printf("已生成 %s，编辑后运行 `gofer config validate` 校验\n", path)
		if usedGlobal {
			c.Printf("提示: export GOFER_CONFIG=%s 后任意目录可用\n", path)
		}
	}
	return nil
}

// runInitSkill installs the embedded gofer-usage/ skill tree under the resolved
// skills parent dir (preserving structure). Landing point: --output <dir> writes
// <dir>/gofer-usage/ (-o is the skills parent), --global writes
// ~/.claude/skills/gofer-usage/, otherwise <cwd>/.claude/skills/gofer-usage/. It
// refuses to overwrite an existing skill dir unless --force is given (design D6),
// mirroring the config-init overwrite guard.
func runInitSkill(c *gcli.Command) error {
	parent, usedGlobal, err := skillDestParent()
	if err != nil {
		return errorx.Failf(configExitErr, "resolve skill dir: %v", err)
	}
	skillDir := filepath.Join(parent, skills.SkillDirName)

	if !initOpts.force {
		if _, err := os.Stat(skillDir); err == nil {
			return errorx.Failf(configExitErr, "skill %s already exists; use --force to overwrite", skillDir)
		} else if !os.IsNotExist(err) {
			return errorx.Failf(configExitErr, "stat %s: %v", skillDir, err)
		}
	}

	written, err := skills.InstallTo(parent)
	if err != nil {
		return errorx.Failf(configExitErr, "write skill: %v", err)
	}

	abs := skillDir
	if a, absErr := filepath.Abs(skillDir); absErr == nil {
		abs = a
	}
	c.Printf("已安装 gofer-usage skill 到 %s (%d 个文件):\n", abs, len(written))
	for _, f := range written {
		c.Printf("  %s\n", f)
	}
	if usedGlobal {
		c.Printf("提示: 全局 skill 对所有项目可见\n")
	}
	return nil
}

// skillDestParent resolves the skills PARENT dir for `gofer init skill` (the
// gofer-usage/ dir is created underneath it). --output wins (it points at the
// skills parent directly); else --global → ~/.claude/skills; else the CWD's
// .claude/skills (project-level). usedGlobal drives the trailing global hint.
func skillDestParent() (parent string, usedGlobal bool, err error) {
	if initOpts.config != "" {
		return initOpts.config, false, nil
	}
	if initOpts.global {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", false, herr
		}
		return filepath.Join(home, ".claude", "skills"), true, nil
	}
	cwd, cerr := os.Getwd()
	if cerr != nil {
		return "", false, cerr
	}
	return filepath.Join(cwd, ".claude", "skills"), false, nil
}

// globalConfigPath resolves the user-global config path for an init target:
// worker → <config-dir>/worker.yaml (what `gofer worker` auto-discovers), server
// (and the bare default) → <config-dir>/config.yaml.
func globalConfigPath(target string) (string, error) {
	if target == "worker" {
		return config.UserWorkerConfigPath()
	}
	return config.UserConfigPath()
}

// NewConfigCmd builds the `config` command group. v1 exposes `config validate`,
// the global counterpart to `project validate <key>`: it loads the config via
// the lookup chain (including the structural validate(cfg) check) and runs the
// per-project filesystem/reference checks for every registered project.
func NewConfigCmd() *gcli.Command {
	return &gcli.Command{
		Name:    "config",
		Desc:    "Inspect and validate the gofer configuration",
		Aliases: []string{"cfg"},
		Subs: []*gcli.Command{
			{
				Name:    "validate",
				Desc:    "Validate a config (target: server | worker): load + per-project paths/agents/runners",
				Aliases: []string{"check"},
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.AddArg("target", "what to validate: server (default) | worker", false)
				},
				Func: runConfigValidate,
			},
			{
				Name: "show",
				Desc: "Show the effective (overlay-merged) config of a project",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.AddArg("key", "project key", true)
				},
				Func: runConfigShow,
			},
			{
				Name: "edit",
				Desc: "Open the resolved config file in $VISUAL/$EDITOR (or code/vim/nano)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
				},
				Func: runConfigEdit,
			},
			{
				Name: "info",
				Desc: "Show the resolved config path, key ENV and key settings",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
				},
				Func: runConfigInfo,
			},
		},
	}
}

// configEditors is the editor lookup chain for `config edit` (in priority order):
// the user's $VISUAL / $EDITOR first, then common interactive editors. The first
// one found on PATH is launched attached to the tty.
var configEditors = []string{"code", "vim", "nano"}

// runConfigEdit resolves the config file path (lookup chain) and opens it in an
// editor. It does NOT decode the config — a config that fails to parse is exactly
// when you most need to edit it — so it only resolves the path. When no config is
// found it tells the operator to scaffold one first (gofer init).
func runConfigEdit(c *gcli.Command, _ []string) error {
	path, err := config.Resolve(config.InputCfgFile)
	if err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}
	if path == "" {
		return errorx.Failf(configExitErr, "no config file found; run `gofer init` (or `gofer init --global`) first")
	}

	ed, tried := resolveEditor()
	if ed == "" {
		return errorx.Failf(configExitErr,
			"no usable editor found (tried %s); set $EDITOR or install one", strings.Join(tried, ", "))
	}

	c.Printf("editing %s with %s\n", path, ed)
	cmd := exec.Command(ed, path)
	// Hand the tty to the editor so an interactive editor (vim/nano) works.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if runErr := cmd.Run(); runErr != nil {
		return errorx.Failf(configExitErr, "editor %s failed: %v", ed, runErr)
	}
	return nil
}

// resolveEditor probes $VISUAL / $EDITOR (the env-driven user preference) before
// the built-in fallbacks (configEditors), returning the first candidate actually
// present on PATH and the list of names tried (for the not-found error message).
// ed=="" means no usable editor was found.
func resolveEditor() (ed string, tried []string) {
	candidates := []string{os.Getenv("VISUAL"), os.Getenv("EDITOR")}
	candidates = append(candidates, configEditors...)

	for _, cand := range candidates {
		cand = strings.TrimSpace(cand)
		if cand == "" {
			continue
		}
		tried = append(tried, cand)
		if _, lookErr := exec.LookPath(cand); lookErr != nil {
			continue
		}
		return cand, tried
	}
	return "", tried
}

// runConfigInfo prints a diagnostic snapshot tailored to the node role
// (GOFER_RUN_MODE): server mode shows the serve config.yaml view; worker mode
// shows the worker.yaml view (worker_id / hub link(s) / served projects), matching
// how `project`/`config validate` already branch on run mode. The token is NEVER
// printed — only whether it is set (SR403) — and neither is any secret value.
func runConfigInfo(c *gcli.Command, _ []string) error {
	mode := config.RunMode()

	c.Println("run_mode:")
	c.Printf("  %s (%s=%s)\n", mode, config.EnvRunMode, envOrUnset(config.EnvRunMode))

	c.Println("env:")
	c.Printf("  %s=%s\n", config.EnvConfigPath, envOrUnset(config.EnvConfigPath))
	c.Printf("  %s=%s\n", config.EnvConfigDir, envOrUnset(config.EnvConfigDir))
	c.Printf("  GOFER_TOKEN set=%s\n", envSetYesNo("GOFER_TOKEN"))

	var err error
	if mode == config.RunModeWorker {
		err = printWorkerConfigInfo(c)
	} else {
		err = printServerConfigInfo(c)
	}
	if err != nil {
		return err
	}

	// Client submit target: `job`/`wf`/`mcp` connect HERE (GOFER_SERVER_ADDR/TOKEN
	// from env/.env), distinct from the serve BIND address. Relevant in both roles
	// (a worker node can still run job/wf CLI). SR403: report only whether set.
	c.Println("client (job/wf submit target):")
	c.Printf("  GOFER_SERVER_ADDR=%s\n", envOrUnset("GOFER_SERVER_ADDR"))
	c.Printf("  GOFER_SERVER_TOKEN set=%s\n", envSetYesNo("GOFER_SERVER_TOKEN"))
	return nil
}

// printServerConfigInfo prints the serve/config.yaml view (default role).
func printServerConfigInfo(c *gcli.Command) error {
	cfg, path, err := config.Load(config.InputCfgFile)
	if err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}
	c.Println("config path:")
	if path == "" {
		c.Println("  (none — defaults + discovery)")
	} else {
		c.Printf("  %s\n", path)
	}
	pathView := cfg.Server.PathView
	if pathView == "" {
		pathView = "host"
	}
	c.Println("settings (server):")
	c.Printf("  server.addr:  %s\n", cfg.Server.Addr)
	c.Printf("  path_view:    %s\n", pathView)
	c.Printf("  web_enabled:  %v\n", cfg.Server.IsWebEnabled())
	c.Printf("  db_path:      %s\n", cfg.ResolveDBPath())
	c.Printf("  projects:     %d\n", len(cfg.Projects))
	c.Printf("  agents:       %d\n", len(cfg.Agents))
	c.Printf("  runners:      %d\n", len(cfg.Runners))
	return nil
}

// printWorkerConfigInfo prints the worker.yaml view (GOFER_RUN_MODE=worker):
// worker_id, the hub link(s) it dials, whether its bearer token resolves, and the
// projects/agents/runners it can run. A missing/undecodable worker.yaml is shown
// as a hint (not a hard error) so `config info` stays a diagnostic aid.
func printWorkerConfigInfo(c *gcli.Command) error {
	path, _ := config.UserWorkerConfigPath()
	c.Println("config path:")
	c.Printf("  %s\n", dashIfEmpty(path))

	wc, err := loadWorkerConfig(config.InputCfgFile)
	if err != nil {
		c.Println("settings (worker):")
		c.Printf("  worker.yaml: %v\n", err)
		c.Println("  hint: run `gofer init worker` to scaffold, or pass --worker-config <path>")
		return nil
	}
	c.Println("settings (worker):")
	c.Printf("  worker_id:         %s\n", dashIfEmpty(wc.WorkerID))
	c.Printf("  server_link.urls:  %s\n", dashIfEmpty(strings.Join(wc.ServerLink.URLs, ", ")))
	c.Printf("  server_link.token set=%s\n", boolYesNo(resolveWorkerToken(wc.ServerLink) != ""))
	c.Printf("  max_concurrent:    %d\n", wc.MaxConcurrent)
	c.Printf("  labels:            %s\n", dashIfEmpty(strings.Join(wc.Labels, ", ")))
	// mode (T6-B): roots present ⇒ POLICY (server pushes projects); else projects ⇒
	// LEGACY (local, deprecated); else EMPTY (nothing to run).
	c.Printf("  mode:              %s\n", workerModeLabel(wc))
	c.Printf("  roots:             %d\n", len(wc.Roots))
	c.Printf("  guards:            allow_exec=%s allow_interactive=%s\n",
		boolPtrStr(wc.Guards.AllowExec), boolPtrStr(wc.Guards.AllowInteractive))
	c.Printf("  projects:          %d (local; ignored in policy mode)\n", len(wc.Projects))
	c.Printf("  agents:            %d\n", len(wc.Agents))
	c.Printf("  runners:           %d\n", len(wc.Runners))
	return nil
}

// workerModeLabel renders workerModeOf as a stable operator-facing string used by
// `config info` (the doctor branches on the same modes).
func workerModeLabel(wc *config.WorkerConfig) string {
	switch workerModeOf(wc) {
	case modePolicy:
		return "policy (roots set; server pushes projects)"
	case modeEmpty:
		return "empty (no roots and no projects — runs nothing)"
	default:
		return "legacy (local projects; deprecated)"
	}
}

// envSetYesNo reports whether an env var is set to a non-empty value (SR403: used
// to disclose token presence without printing its value). boolYesNo maps a bool.
func envSetYesNo(name string) string { return boolYesNo(os.Getenv(name) != "") }

func boolYesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// dashIfEmpty renders an em-dash for empty diagnostic values so blank rows read as
// "unset" rather than a formatting glitch.
func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// envOrUnset returns the env var value, or a "(unset)" sentinel when empty, so
// `config info` distinguishes "set to empty" from "not set" readably.
func envOrUnset(name string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return "(unset)"
}

// runConfigShow prints the effective (overlay-merged) config of one project. It
// EXPLICITLY applies the per-project overlays (config validate does NOT merge
// overlay — P2/D2: the裁决真源是全局 config; this is a diagnostic view, NOT a
// ruling) so an operator can see what serve would actually use. It must run on
// the same host/container as serve to read the overlay files (otherwise it only
// shows the global values — hence the trailing hint).
func runConfigShow(c *gcli.Command, _ []string) error {
	key := ""
	if a := c.Arg("key"); a != nil {
		key = a.String()
	}
	if key == "" {
		return errorx.Failf(configExitErr, "config show requires a <key> argument")
	}

	cfg, _, err := config.Load(config.InputCfgFile)
	if err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}
	// Diagnostic merge: surface overlay-merged effective config (NOT a ruling, D2).
	for _, w := range config.ApplyProjectOverlays(cfg) {
		c.Printf("overlay warn: %s\n", w)
	}

	p, ok := cfg.Projects[key]
	if !ok {
		return errorx.Failf(configExitErr, "project %q not registered", key)
	}

	c.Printf("project: %s\n", key)
	c.Printf("host_path: %s\n", p.HostPath)
	c.Printf("container_path: %s\n", p.ContainerPath)
	c.Printf("exchange_subdir: %s\n", cfg.ResolvedExchangeSubdir(p))
	c.Printf("result_subdir: %s\n", cfg.ResolvedResultSubdir(p))
	c.Printf("default_agent: %s\n", p.DefaultAgent)
	c.Printf("allowed_agents: %v\n", p.AllowedAgents)
	c.Printf("allowed_runners: %v\n", p.AllowedRunners)
	c.Printf("allow_exec: %v\n", p.AllowExec)
	c.Printf("max_concurrent_jobs: %d\n", p.MaxConcurrentJobs)
	c.Printf("capture_diff: %s\n", boolPtrStr(p.CaptureDiff))
	c.Printf("notify_enabled: %s\n", boolPtrStr(p.NotifyEnabled))
	c.Println("(诊断视图: overlay 须在 serve 同机/容器内可读才生效; 否则仅显示全局值)")
	return nil
}

// boolPtrStr renders an optional *bool overlay/config field: nil prints as
// "(default)" (the field falls back to its IsXxx default), otherwise true/false.
func boolPtrStr(b *bool) string {
	if b == nil {
		return "(default)"
	}
	if *b {
		return "true"
	}
	return "false"
}

// runConfigValidate dispatches by target: `server` (default) validates the server
// config; `worker` validates a worker.yaml. Backward compatible — a bare
// `gofer config validate` still validates the server config.
func runConfigValidate(c *gcli.Command, _ []string) error {
	target := "server"
	if a := c.Arg("target"); a != nil && a.String() != "" {
		target = strings.ToLower(a.String())
	}
	switch target {
	case "", "server":
		return validateServerConfig(c)
	case "worker":
		return validateWorkerConfig(c)
	default:
		return errorx.Failf(configExitErr, "unknown validate target %q (use: server | worker)", target)
	}
}

// validateServerConfig loads the server config (which runs Load's structural
// validate) and iterates every project running the same per-project checks as
// `project validate <key>` (reusing loadRegistry + reg.Validate, not a reimpl).
// One [OK]/[FAIL] line per check; any FAIL → coded error (non-zero exit).
func validateServerConfig(c *gcli.Command) error {
	// loadRegistry calls config.Load, which already runs validate(cfg) (host_path
	// required) and surfaces decode errors — a coded error so the exit is non-zero.
	reg, err := loadRegistry(config.InputCfgFile)
	if err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}

	if p := reg.Path(); p != "" {
		ccolor.Infof("validate config: %s\n", p)
	} else {
		ccolor.Infof("validate config: (built-in defaults — no config file found)\n")
	}
	keys := reg.List()
	if len(keys) == 0 {
		c.Println("(no projects registered)")
		c.Println("config OK")
		return nil
	}
	if validateProjects(c, reg, keys) {
		c.Println("config OK")
		return nil
	}
	return errorx.Failf(configExitErr, "config validation failed")
}

// Doctor line statuses. OK/FAIL keep their historical spellings (padded to 4 so
// the fixed-width column aligns); WARN/INFO are advisory — they NEVER fail the
// command (only FAIL sets a non-zero exit).
const (
	docStatusOK   = "OK  "
	docStatusFail = "FAIL"
	docStatusWarn = "WARN"
	docStatusInfo = "INFO"
)

// migrationDoc is the in-repo path the LEGACY deprecation WARN points operators at
// (T6-D). Kept as a const so the WARN text and the shipped doc never drift.
const migrationDoc = "docs/runbook/2026-07-15-worker-policy-migration.md"

// validateWorkerConfig decodes a worker.yaml and checks the worker-specific
// fields (worker_id / server_link.urls / token resolvable), then branches on the
// worker's project-sourcing MODE (T6-B):
//   - LEGACY (local projects, no roots): today's per-project checks, plus a WARN
//     that `projects:` is deprecated (server-pushed policy is the future) — it
//     still works, so this never fails.
//   - POLICY (roots present): projects come from the SERVER, so 0 LOCAL projects is
//     NOT a failure. Validate the roots (to exists / from set / no dup / overlap)
//     and the guards instead, and report the effective project count (INFO).
//   - EMPTY (neither): FAIL — the worker can run nothing (unchanged behaviour).
//
// Any FAIL → coded error (non-zero exit).
func validateWorkerConfig(c *gcli.Command) error {
	wc, err := loadWorkerConfig(config.InputCfgFile)
	if err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}

	allOK := true
	line := func(status, name, info string) {
		if status == docStatusFail {
			allOK = false
		}
		c.Printf("[%s] worker/%-18s %s\n", status, name, info)
	}
	chk := func(name string, ok bool, info string) {
		if ok {
			line(docStatusOK, name, info)
		} else {
			line(docStatusFail, name, info)
		}
	}

	chk("worker_id", wc.WorkerID != "", wc.WorkerID+" (must equal a server.workers key)")
	chk("server_link.urls", len(wc.ServerLink.URLs) > 0, fmt.Sprintf("%v", wc.ServerLink.URLs))
	// Token resolvable: token_env (env set) OR an inline token. An unresolvable
	// token is the #1 connect failure (worker is 401'd / register-rejected).
	tokInfo := "inline token"
	if wc.ServerLink.TokenEnv != "" {
		tokInfo = "token_env=" + wc.ServerLink.TokenEnv
	}
	chk("server_link.token", resolveWorkerToken(wc.ServerLink) != "",
		tokInfo+" (must equal server.workers."+wc.WorkerID+" token)")

	switch workerModeOf(wc) {
	case modePolicy:
		validateWorkerRoots(c, wc, line)
		validateWorkerGuards(wc, line)
		n, note := effectivePolicyProjectCount(wc)
		line(docStatusInfo, "projects",
			fmt.Sprintf("projects 由 server 下发; 当前生效 %d 个 (读自 policy 缓存%s)", n, note))
	case modeEmpty:
		chk("projects", false, "无 roots(policy 模式) 也无 projects(legacy) — 这台 worker 跑不了任何 job")
	default: // modeLegacy
		line(docStatusWarn, "projects",
			"`projects:` 段已废弃(策略改由 server 下发)，迁移见 "+migrationDoc+"; 本版本仍然生效")
		if !validateWorkerLocalProjects(c, wc, chk) {
			allOK = false
		}
	}

	if !allOK {
		return errorx.Failf(configExitErr, "worker config validation failed")
	}
	c.Println("worker config OK")
	return nil
}

// validateWorkerLocalProjects runs the per-project checks for a LEGACY worker: a
// registry built from the worker's OWN config (host_path exists, agents/runners
// resolvable — identical to server-side project validate) plus the worker-specific
// local-runner rule. Resolved through agent.Resolve (P2 T0-C) so the doctor's agent
// view is the one the worker will actually run with. Read-only (never Add/save).
// Returns whether every project passed.
func validateWorkerLocalProjects(c *gcli.Command, wc *config.WorkerConfig, chk func(string, bool, string)) bool {
	wdcfg, _ := agent.Resolve(workerConfigToConfig(wc), agent.DefaultDetector())
	reg := project.NewRegistry(wdcfg, config.InputCfgFile)
	keys := reg.List()
	if len(keys) == 0 {
		chk("projects", false, "no projects (the worker has nothing to run)")
		return false
	}
	sort.Strings(keys)
	ok := validateProjects(c, reg, keys)
	for _, key := range keys {
		// Worker-specific: a worker executes dispatched jobs LOCALLY, so each of its
		// projects must allow the built-in `local` runner (not the server's worker
		// runner name — a common copy-paste mistake).
		p, _ := reg.Get(key)
		if !allowsLocalRunner(p.AllowedRunners) {
			chk(key+"/local-runner", false,
				"worker runs locally → allowed_runners should include local")
			ok = false
		} else {
			chk(key+"/local-runner", true,
				"worker runs locally → allowed_runners should include local")
		}
	}
	return ok
}

// validateWorkerRoots checks a POLICY worker's roots: each root's `to` must be an
// existing directory and its `from` must be set; duplicate `from` prefixes are a
// FAIL (ambiguous mapping); overlapping roots (one `from` a boundary-aligned prefix
// of another) are an INFO reminder that the LONGEST prefix wins (§6-H3 — the
// "更具体者优先" override is a first-class case, not a bug).
func validateWorkerRoots(c *gcli.Command, wc *config.WorkerConfig, line func(string, string, string)) {
	if len(wc.Roots) == 0 {
		// modePolicy implies len(Roots)>0, so this only fires on a future refactor —
		// keep it loud rather than silently skipping the roots check.
		line(docStatusFail, "roots", "policy 模式需要至少一条 root (roots 为空)")
		return
	}
	seen := map[string]bool{}
	for i, r := range wc.Roots {
		name := fmt.Sprintf("roots[%d]", i)
		from := normRootPrefix(r.From)
		to := strings.TrimSpace(r.To)
		if from == "" {
			line(docStatusFail, name, "from 不能为空")
			continue
		}
		if to == "" {
			line(docStatusFail, name, r.From+" -> (to 不能为空)")
			continue
		}
		if seen[from] {
			line(docStatusFail, name, "重复的 from: "+r.From+" (每个 from 只能映射一次)")
			continue
		}
		seen[from] = true
		if fi, statErr := os.Stat(to); statErr != nil || !fi.IsDir() {
			line(docStatusFail, name, fmt.Sprintf("%s -> %s (to 目录不存在或不是目录)", r.From, to))
			continue
		}
		line(docStatusOK, name, fmt.Sprintf("%s -> %s", r.From, to))
	}
	// Overlap hint (informational): a shorter from that is a boundary-aligned prefix
	// of a longer one is intentional (最长前缀覆盖 = per-project override, §6-H3).
	for i := range wc.Roots {
		for j := range wc.Roots {
			if i == j {
				continue
			}
			a, b := normRootPrefix(wc.Roots[i].From), normRootPrefix(wc.Roots[j].From)
			if a != "" && b != "" && a != b && isBoundaryPrefix(a, b) {
				line(docStatusInfo, "roots/overlap",
					fmt.Sprintf("%s 覆盖 %s 下的更长路径 (最长前缀/更具体者优先)", wc.Roots[j].From, wc.Roots[i].From))
			}
		}
	}
}

// validateWorkerGuards warns when the guards block is entirely unset (both fields
// nil): that is the "do NOT tighten" default — every exec/interactive job runs — so
// it is not a failure, but an operator migrating to policy mode should DECLARE the
// posture explicitly rather than inherit the permissive default (T6-B / T6-D step ①).
func validateWorkerGuards(wc *config.WorkerConfig, line func(string, string, string)) {
	if wc.Guards.AllowExec == nil && wc.Guards.AllowInteractive == nil {
		line(docStatusWarn, "guards",
			"护栏未设置 = 不额外收紧(exec/interactive 全放行); 建议显式声明 allow_exec / allow_interactive")
		return
	}
	line(docStatusOK, "guards",
		fmt.Sprintf("allow_exec=%s allow_interactive=%s",
			boolPtrStr(wc.Guards.AllowExec), boolPtrStr(wc.Guards.AllowInteractive)))
}

// effectivePolicyProjectCount reads the POLICY worker's last-known-good cache and
// returns how many projects would currently be EFFECTIVE (projected onto its roots,
// path_outside_roots dropped). A missing/unusable cache is NOT an error — the worker
// is simply not running or has not received a policy yet — so it returns (0, note).
func effectivePolicyProjectCount(wc *config.WorkerConfig) (int, string) {
	p, err := worker.ReadPolicyCacheFile(workerPolicyCachePath(wc.WorkerID), wc.WorkerID)
	if err != nil {
		return 0, ", 缓存不可用: " + err.Error()
	}
	if p == nil {
		return 0, ", worker 未运行或尚未收到 Policy"
	}
	cfg, _ := projectPolicy(wc, *p)
	return len(cfg.Projects), ""
}

// normRootPrefix normalizes a roots `from`/`to` prefix for comparison: backslashes
// → '/', trailing slashes trimmed (a lone "/" survives). It mirrors the config
// package's internal normalizeLogicalPath closely enough for the doctor's dup/overlap
// checks (the authoritative mapping still lives in config.MapRoot).
func normRootPrefix(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\\", "/")
	for len(s) > 1 && strings.HasSuffix(s, "/") {
		s = s[:len(s)-1]
	}
	return s
}

// isBoundaryPrefix reports whether prefix is a strict, segment-aligned prefix of p
// (so /a/b matches /a/b/c but NOT /a/bc). Both are assumed already normRootPrefix'd.
// Case-sensitive — a conservative overlap HINT does not need MapRoot's Windows
// case-folding.
func isBoundaryPrefix(prefix, p string) bool {
	if prefix == "/" {
		return strings.HasPrefix(p, "/") && p != "/"
	}
	if len(p) <= len(prefix) || !strings.HasPrefix(p, prefix) {
		return false
	}
	return p[len(prefix)] == '/'
}

// validateProjects prints one [OK]/[FAIL] line per per-project check and returns
// whether all projects passed. Shared by server + worker validation.
func validateProjects(c *gcli.Command, reg *project.Registry, keys []string) bool {
	allOK := true
	for _, key := range keys {
		results, ok, vErr := reg.Validate(key)
		if vErr != nil {
			c.Printf("[FAIL] %-22s %v\n", key, vErr)
			allOK = false
			continue
		}
		for _, res := range results {
			status := "OK  "
			if !res.OK {
				status = "FAIL"
			}
			c.Printf("[%s] %s/%-18s %s\n", status, key, res.Name, res.Info)
		}
		if !ok {
			allOK = false
		}
	}
	return allOK
}

// allowsLocalRunner reports whether the allowlist permits the built-in local
// runner: an empty allowlist defaults to local, otherwise it must be listed.
func allowsLocalRunner(allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, r := range allowed {
		if r == "local" {
			return true
		}
	}
	return false
}
