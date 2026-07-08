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
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/project"
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
// `server` (default) or `worker`; it writes the matching embedded starter to its
// default path (./.gofer.yaml / ./worker.yaml) or --output <path>. It refuses to
// overwrite an existing file unless --force is given (design D6).
func NewInitCmd() *gcli.Command {
	return &gcli.Command{
		Name: "init",
		Desc: "Generate a starter config from the example template (target: server | worker)",
		Config: func(c *gcli.Command) {
			c.AddArg("target", "what to scaffold: server (default) | worker", false)
			c.StrOpt(&initOpts.config, "output", "o", "", "path to write the config (default depends on target)")
			c.BoolOpt(&initOpts.force, "force", "f", false, "overwrite an existing config file")
			c.BoolOpt(&initOpts.global, "global", "g", false, "write to the user-global config dir (~/.config/gofer/config.yaml)")
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
	tmpl, defaultPath, ok := initTemplate(target)
	if !ok {
		return errorx.Failf(configExitErr, "unknown init target %q (use: server | worker)", target)
	}

	// Path resolution: an explicit --output always wins (backward compatible).
	// Otherwise --global writes the user-global config.yaml for the server target
	// (worker has no global discovery, so it keeps its ./worker.yaml default).
	path := initOpts.config
	usedGlobal := false
	if path == "" {
		if initOpts.global && (target == "" || target == "server") {
			gp, err := config.UserConfigPath()
			if err != nil {
				return errorx.Failf(configExitErr, "resolve global config path: %v", err)
			}
			path = gp
			usedGlobal = true
			_ = os.MkdirAll(filepath.Dir(path), 0o755) // ensure ~/.config/gofer exists
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
		c.Printf("已生成 worker 配置 %s，编辑后运行 `gofer worker --worker-config %s` 启动\n", path, path)
	} else {
		c.Printf("已生成 %s，编辑后运行 `gofer config validate` 校验\n", path)
		if usedGlobal {
			c.Printf("提示: export GOFER_CONFIG=%s 后任意目录可用\n", path)
		}
	}
	return nil
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

// runConfigInfo prints a diagnostic snapshot: the resolved config path, the key
// gofer ENV vars and the key effective settings. The token is NEVER printed —
// only whether it is set (SR403) — and neither is any secret value.
func runConfigInfo(c *gcli.Command, _ []string) error {
	cfg, path, err := config.Load(config.InputCfgFile)
	if err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}

	c.Println("config path:")
	if path == "" {
		c.Printf("  (none — defaults + discovery)\n")
	} else {
		c.Printf("  %s\n", path)
	}

	c.Println("env:")
	c.Printf("  %s=%s\n", config.EnvConfigPath, envOrUnset(config.EnvConfigPath))
	c.Printf("  %s=%s\n", config.EnvConfigDir, envOrUnset(config.EnvConfigDir))
	// SR403: never print the token value — only report whether it is set.
	tokenSet := "no"
	if os.Getenv("GOFER_TOKEN") != "" {
		tokenSet = "yes"
	}
	c.Printf("  GOFER_TOKEN set=%s\n", tokenSet)

	pathView := cfg.Server.PathView
	if pathView == "" {
		pathView = "host"
	}
	c.Println("settings:")
	c.Printf("  server.addr:  %s\n", cfg.Server.Addr)
	c.Printf("  path_view:    %s\n", pathView)
	c.Printf("  web_enabled:  %v\n", cfg.Server.IsWebEnabled())
	c.Printf("  db_path:      %s\n", cfg.ResolveDBPath())
	c.Printf("  projects:     %d\n", len(cfg.Projects))
	c.Printf("  agents:       %d\n", len(cfg.Agents))
	c.Printf("  runners:      %d\n", len(cfg.Runners))

	// Client submit target: `job`/`wf`/`mcp` connect HERE (GOFER_SERVER_ADDR/TOKEN
	// from env/.env), which is distinct from settings.server.addr above (the serve
	// BIND address). Surfacing it answers "where do my `job` commands actually go".
	// SR403: report only whether the token is set, never its value.
	serverTokenSet := "no"
	if os.Getenv("GOFER_SERVER_TOKEN") != "" {
		serverTokenSet = "yes"
	}
	c.Println("client (job/wf submit target):")
	c.Printf("  GOFER_SERVER_ADDR=%s\n", envOrUnset("GOFER_SERVER_ADDR"))
	c.Printf("  GOFER_SERVER_TOKEN set=%s\n", serverTokenSet)
	return nil
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

// validateWorkerConfig decodes a worker.yaml and checks the worker-specific
// fields (worker_id / server_link.urls / token resolvable) plus every project's
// paths/agents/runners (reusing the project registry built from the worker's own
// config — the same checks the worker runs at dispatch time, review #8). Any FAIL
// → coded error (non-zero exit).
func validateWorkerConfig(c *gcli.Command) error {
	wc, err := loadWorkerConfig(config.InputCfgFile)
	if err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}

	allOK := true
	chk := func(name string, ok bool, info string) {
		status := "OK  "
		if !ok {
			status = "FAIL"
			allOK = false
		}
		c.Printf("[%s] worker/%-18s %s\n", status, name, info)
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

	// Per-project checks via a registry built from the worker's own config (host_path
	// exists, agents/runners resolvable) — identical to server-side project validate.
	reg := project.NewRegistry(workerConfigToConfig(wc), config.InputCfgFile)
	keys := reg.List()
	if len(keys) == 0 {
		chk("projects", false, "no projects (the worker has nothing to run)")
	} else {
		sort.Strings(keys)
		if !validateProjects(c, reg, keys) {
			allOK = false
		}
		for _, key := range keys {
			// Worker-specific: a worker executes dispatched jobs LOCALLY, so each of its
			// projects must allow the built-in `local` runner (not the server's worker
			// runner name — a common copy-paste mistake).
			p, _ := reg.Get(key)
			chk(key+"/local-runner", allowsLocalRunner(p.AllowedRunners),
				"worker runs locally → allowed_runners should include local")
		}
	}

	if !allOK {
		return errorx.Failf(configExitErr, "worker config validation failed")
	}
	c.Println("worker config OK")
	return nil
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
