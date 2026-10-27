package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/gookit/goutil/x/ccolor"
	"github.com/inhere/gofer/internal/buildinfo"

	configtmpl "github.com/inhere/gofer/config"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/configlint"
	"github.com/inhere/gofer/internal/hookrelay"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/notify"
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
	config    string
	force     bool
	global    bool
	agent     string
	remove    bool
	primeOnly bool
	workspace string
	// Flags accepted only to provide a migration error for the removed alias. They are
	// hidden from `gofer init --help`; the onboarding wizard is `gofer worker init`.
	server     string
	token      string
	adminToken string
	id         string
	roots      gcli.Strings
	projects   gcli.Strings
	yes        bool
	timeout    string
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

// clientEnvTemplate is the starter dotenv `gofer init client` writes for a pure
// client node (GOFER_RUN_MODE=client). Unlike the server/worker targets this is not
// a gofer config at all: a client node has NO local config.yaml / worker.yaml, it
// only carries the connection to a remote server — which is exactly what these keys
// hold. The three keys are left empty on purpose (the operator fills in the address
// and token); only the role is preset.
const clientEnvTemplate = `# gofer client node (GOFER_RUN_MODE=client)
#
# A client node has NO local config.yaml / worker.yaml: it only carries the
# connection to a remote gofer server, so job/workflow/mcp/project/agent all talk
# to that server. gofer loads this file automatically from $GOFER_CONFIG_DIR/.env.
#
# Server the CLI connects to, e.g. 127.0.0.1:8765 or host.docker.internal:8765.
GOFER_SERVER_ADDR=
# Bearer token of that server (never commit a real token).
GOFER_SERVER_TOKEN=
# This node's role: client → never load a local gofer config.
GOFER_RUN_MODE=client
`

// clientEnvContent is what `gofer init client` writes: the starter above plus the
// language hint (design 2026-10-27-i18n-design.md §3 — a client node keeps its
// language in this .env, which config.LoadDotenv exports before the CLI is built).
// The hint comment is written in the language the init ran in.
func clientEnvContent() string {
	return clientEnvTemplate + "# " + cliText("cli.setup.init.client_env_lang") + "\n# GOFER_LANG=en\n"
}

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
// `server` (default), `worker`, `client`, or `skill`. For server/worker it writes
// the matching embedded starter to its default path (./.gofer.yaml / worker.yaml)
// or --output <path>; for client it writes the client node's <config-dir>/.env
// starter (the node holds only the connection env — no config file); for skill it
// installs the embedded gofer-usage/ tree to BOTH .claude/skills/ and
// .agents/skills/ (--output narrows to one dir). It refuses to overwrite an
// existing target unless --force is given (design D6).
//
// `gofer init worker` always writes the embedded template. Worker onboarding is
// intentionally a separate command: `gofer worker init`.
func NewInitCmd(info buildinfo.Info) *gcli.Command {
	return &gcli.Command{
		Name: "init",
		Desc: "Scaffold a starter config, skill or agent hooks from the embedded templates (target: server | worker | client | skill | hooks)",
		Config: func(c *gcli.Command) {
			c.AddArg("target", "what to scaffold: server (default) | worker | client | skill | hooks", false)
			c.StrOpt(&initOpts.config, "output", "o", "", "output path (config file for server/worker; .env for client; single skills parent dir for skill; project dir for hooks — overrides the default)")
			c.BoolOpt(&initOpts.force, "force", "f", false, "overwrite an existing config file or skill (hooks: replace an unparsable hook config)")
			c.BoolOpt(&initOpts.global, "global", "g", false, "write to the user-global dir (<config-dir>/config.yaml|worker.yaml|.env for server/worker/client; skill installs to ~/.claude/skills and ~/.agents/skills; hooks to ~/.claude, ~/.codex, ~/.omp/agent/extensions and ~/.jcode/config.toml)")
			c.StrOpt(&initOpts.agent, "agent", "a", "claude", "hooks: which agent config to write: claude | codex | omp | jcode | all (all = the four, only for agents whose config dir already exists; skill: all = only existing .claude / .agents; jcode has no project level, needs --global or -o <JCODE_HOME dir>)")
			c.BoolOpt(&initOpts.remove, "remove", "", false, "hooks: remove gofer's hook entries instead of installing them")
			c.BoolOpt(&initOpts.primeOnly, "prime-only", "", false, "hooks: install or remove only the memory injection (SessionStart prime + PreToolUse command memories)")
			c.StrOpt(&initOpts.workspace, "workspace", "", "", "server: directory to register as the `default` project (default: $GOFER_WORKSPACE, else ~/.gofer/workspace)")
			// Keep these hidden long enough to turn old invocations into an actionable
			// migration error; the wizard itself lives under `gofer worker init`.
			c.StrOpt(&initOpts.server, "server", "s", "", "worker: use `gofer worker init` for onboarding")
			c.StrOpt(&initOpts.token, "token", "", "", "worker: use `gofer worker init` for onboarding")
			c.StrOpt(&initOpts.adminToken, "admin-token", "", "", "worker: use `gofer worker init` for onboarding")
			c.StrOpt(&initOpts.id, "id", "", "", "worker: use `gofer worker init` for onboarding")
			c.VarOpt(&initOpts.roots, "roots", "", "worker: use `gofer worker init` for onboarding")
			c.VarOpt(&initOpts.projects, "project", "", "worker: use `gofer worker init` for onboarding")
			c.BoolOpt(&initOpts.yes, "yes", "y", false, "worker: use `gofer worker init` for onboarding")
			c.StrOpt(&initOpts.timeout, "timeout", "", "", "worker: use `gofer worker init` for onboarding")
			for _, name := range []string{"server", "token", "admin-token", "id", "roots", "project", "yes", "timeout"} {
				c.Opt(name).Hidden = true
			}
		},
		Func: func(c *gcli.Command, _ []string) error { return runInit(c, info) },
	}
}

// initConfigLanguage rewrites the starter template's `server.language` to the
// machine's language, so a fresh install starts in the operator's language instead
// of the DEPRECATED default (design 2026-10-27-i18n-design.md §3). A template
// without the line (or an unrecognized locale, which reads as the default) is
// written unchanged.
func initConfigLanguage(tmpl string) string {
	lang := string(i18n.FromEnv())
	if lang == "" {
		return tmpl
	}
	for _, line := range []string{"\n  language: " + string(i18n.Default) + "\n", "\n  language: \"\"\n"} {
		if strings.Contains(tmpl, line) {
			return strings.Replace(tmpl, line, "\n  language: "+lang+"\n", 1)
		}
	}
	return tmpl
}

// runInit writes the embedded example template for the chosen target to the
// output path. The templates are the single source of truth shared with
// config/{gofer,worker}.example.yaml (no drift). For the server target it also
// creates the default workspace and registers it as the `default` project (F-g).
func runInit(c *gcli.Command, info buildinfo.Info) error {
	target := "server"
	if a := c.Arg("target"); a != nil && a.String() != "" {
		target = strings.ToLower(a.String())
	}
	// skill installs a directory tree (not a single templated file), so it takes a
	// separate path — initTemplate stays config-only (server|worker).
	if target == "skill" {
		return runInitSkill(c)
	}
	if target == "hooks" || target == "hook" {
		return runInitHooks(c)
	}
	if target == "client" {
		return runInitClient(c)
	}
	// `gofer init worker` always writes the template. The onboarding wizard is
	// intentionally not a compatibility alias: use `gofer worker init`.
	if target == "worker" && hasWorkerInitFlags() {
		return rejectLegacyWorkerInit(c)
	}
	tmpl, defaultPath, ok := initTemplate(target)
	if !ok {
		return errorx.Failf(configExitErr, "unknown init target %q (use: server | worker | client | skill | hooks)", target)
	}

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

	// F-g: a generated SERVER config carries the default workspace (created here) as
	// the `default` project, so a fresh install can run a job with no project setup.
	// The worker template stays verbatim.
	if target != "worker" {
		extended, werr := applyDefaultWorkspace(path, tmpl, initOpts.workspace)
		if werr != nil {
			return errorx.Failf(configExitErr, "%v", werr)
		}
		tmpl = initConfigLanguage(extended)
	}

	if err := os.WriteFile(path, []byte(tmpl), 0o644); err != nil {
		return errorx.Failf(configExitErr, "write %s: %v", path, err)
	}
	if target == "worker" {
		if usedGlobal {
			// Global worker.yaml is the path `gofer worker` auto-discovers, so no
			// --worker-config is needed.
			c.Printf("%s\n", cliText("cli.setup.init.worker_global", "path", path))
		} else {
			c.Printf("%s\n", cliText("cli.setup.init.worker", "path", path))
		}
	} else {
		c.Printf("%s\n", cliText("cli.setup.init.server", "path", path))
		if usedGlobal {
			c.Printf("%s\n", cliText("cli.setup.init.server_global_hint", "path", path))
		}
	}
	return nil
}

func hasWorkerInitFlags() bool {
	return strings.TrimSpace(initOpts.server) != "" || strings.TrimSpace(initOpts.token) != "" ||
		strings.TrimSpace(initOpts.adminToken) != "" || strings.TrimSpace(initOpts.id) != "" ||
		len(initOpts.roots) > 0 || len(initOpts.projects) > 0 || initOpts.yes ||
		strings.TrimSpace(initOpts.timeout) != "" || strings.TrimSpace(initOpts.workspace) != ""
}

func rejectLegacyWorkerInit(c *gcli.Command) error {
	args := []string{"gofer", "worker", "init"}
	appendArg := func(name, value string) {
		if strings.TrimSpace(value) != "" {
			args = append(args, name, value)
		}
	}
	appendArg("--server", initOpts.server)
	appendArg("--token", initOpts.token)
	appendArg("--admin-token", initOpts.adminToken)
	appendArg("--id", initOpts.id)
	for _, root := range initOpts.roots {
		appendArg("--roots", root)
	}
	for _, project := range initOpts.projects {
		appendArg("--project", project)
	}
	if initOpts.yes {
		args = append(args, "--yes")
	}
	appendArg("--timeout", initOpts.timeout)
	appendArg("--workspace", initOpts.workspace)
	if initOpts.config != "" {
		appendArg("--output", initOpts.config)
	}
	if initOpts.global {
		args = append(args, "--global")
	}
	if initOpts.force {
		args = append(args, "--force")
	}
	return errorx.Failf(configExitErr, "%s", cliText("cli.setup.init.worker_redirect", "command", strings.Join(args, " ")))
}

// runInitClient writes the starter dotenv
// (GOFER_RUN_MODE=client) — see clientEnvTemplate. Unlike server/worker it is not a
// config file, and its home is fixed by the loader: the client node's connection env
// is <config-dir>/.env (config.EnvFileName under config.ConfigDir), which is exactly
// what `init --global` targets — so the config dir is the DEFAULT here (no -g needed),
// --global spells the same path out, and --output still overrides.
//
// An existing .env is never overwritten: it holds this node's real server token, so
// clobbering it would silently disconnect the node. The refusal is a coded error
// (non-zero exit) and names --force, mirroring init server|worker (design D6).
func runInitClient(c *gcli.Command) error {
	// -g/--global needs no branch: the global dir IS the config dir, and for this
	// target that is already the default (initOpts.global is therefore accepted and
	// has nothing left to change).
	path := initOpts.config
	if path == "" {
		dir, err := config.ConfigDir()
		if err != nil {
			return errorx.Failf(configExitErr, "resolve config dir: %v", err)
		}
		path = filepath.Join(dir, config.EnvFileName)
	}

	if !initOpts.force {
		if _, err := os.Stat(path); err == nil {
			return errorx.Failf(configExitErr,
				"%s already exists; use --force to overwrite (it holds this node's server token)", path)
		} else if !os.IsNotExist(err) {
			return errorx.Failf(configExitErr, "stat %s: %v", path, err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errorx.Failf(configExitErr, "create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(clientEnvContent()), 0o644); err != nil {
		return errorx.Failf(configExitErr, "write %s: %v", path, err)
	}
	c.Printf("%s\n", cliText("cli.setup.init.client_env", "path", path))
	return nil
}

// runInitHooks merges gofer's agent-CLI hook entries (session relay, SESS-01)
// into the Claude Code / Codex hook config of the current project (or the
// user's home with --global, or --output <dir>). The merge is idempotent and
// keeps every foreign entry; --remove takes gofer's entries out again.
func runInitHooks(c *gcli.Command) error {
	var agents []string
	explicit := true
	switch strings.ToLower(initOpts.agent) {
	case "", hookrelay.AgentClaude:
		agents = []string{hookrelay.AgentClaude}
	case hookrelay.AgentCodex:
		agents = []string{hookrelay.AgentCodex}
	case hookrelay.AgentOmp:
		agents = []string{hookrelay.AgentOmp}
	case hookrelay.AgentJcode:
		agents = []string{hookrelay.AgentJcode}
	case "both":
		agents = []string{hookrelay.AgentClaude, hookrelay.AgentCodex}
	case "all":
		agents = []string{hookrelay.AgentClaude, hookrelay.AgentCodex, hookrelay.AgentOmp, hookrelay.AgentJcode}
		explicit = false
	default:
		return errorx.Failf(configExitErr, "unknown --agent %q (use: claude | codex | omp | jcode | all)", initOpts.agent)
	}
	dir := initOpts.config
	var err error
	global := dir == "" && initOpts.global
	switch {
	case dir != "":
	case initOpts.global:
		dir, err = os.UserHomeDir()
	default:
		dir, err = os.Getwd()
	}
	if err != nil {
		return errorx.Failf(configExitErr, "resolve hooks dir: %v", err)
	}
	var installed []string
	for _, agent := range agents {
		if agent == hookrelay.AgentJcode && !global && (initOpts.config == "" || !explicit) {
			// jcode reads one user-level config.toml only; a project directory means nothing to it.
			if explicit {
				return errorx.Failf(configExitErr, "jcode has no project-level hooks: use --global (~/.jcode/config.toml) or -o <JCODE_HOME dir>")
			}
			c.Printf("%s\n", cliText("cli.setup.hooks.skip_jcode"))
			continue
		}
		var path string
		var perr error
		if global {
			path, perr = hookrelay.GlobalConfigFileFor(agent, dir)
		} else {
			path, perr = hookrelay.ConfigFileFor(agent, dir)
		}
		if perr != nil {
			return errorx.Failf(configExitErr, "%v", perr)
		}
		// -a all only writes for agents that are present: an agent whose config dir does not
		// exist is skipped (and listed) instead of getting a fresh, useless dir. A named
		// agent keeps creating its dir.
		if !explicit {
			if cfgDir := hookAgentDir(path); !dirExists(cfgDir) {
				c.Printf("%s\n", cliText("cli.setup.hooks.skip_agent_missing_dir", "agent", agent, "dir", cfgDir))
				continue
			}
		}
		jsonHooks := agent == hookrelay.AgentClaude || agent == hookrelay.AgentCodex
		if initOpts.primeOnly && !jsonHooks {
			c.Printf("%s\n", cliText("cli.setup.hooks.skip_prime_only", "agent", agent))
			continue
		}
		var res hookrelay.InstallResult
		if initOpts.primeOnly {
			if initOpts.remove {
				changed, ierr := hookrelay.RemoveTrackerPrime(agent, dir)
				if ierr != nil {
					return errorx.Failf(configExitErr, "remove %s memory prime: %v", agent, ierr)
				}
				cmdChanged, ierr := hookrelay.RemoveCommandMemory(agent, dir)
				if ierr != nil {
					return errorx.Failf(configExitErr, "remove %s command memory hook: %v", agent, ierr)
				}
				res = hookrelay.InstallResult{Path: path}
				if changed {
					res.Removed = 1
				}
				if cmdChanged {
					res.Removed++
				}
			} else {
				changed, ierr := hookrelay.InstallTrackerPrime(agent, dir)
				if ierr != nil {
					return errorx.Failf(configExitErr, "install %s memory prime: %v", agent, ierr)
				}
				cmdChanged, ierr := hookrelay.InstallCommandMemory(agent, dir)
				if ierr != nil {
					return errorx.Failf(configExitErr, "install %s command memory hook: %v", agent, ierr)
				}
				res = hookrelay.InstallResult{Path: path}
				if changed {
					res.Added = 1
				}
				if cmdChanged {
					res.Added++
				}
			}
		} else {
			var ierr error
			res, ierr = hookrelay.Install(agent, path, initOpts.remove, initOpts.force)
			if ierr != nil {
				return errorx.Failf(configExitErr, "install %s hooks: %v", agent, ierr)
			}
			if !initOpts.remove && jsonHooks {
				if _, ierr := hookrelay.InstallTrackerPrime(agent, dir); ierr != nil {
					return errorx.Failf(configExitErr, "install %s memory prime: %v", agent, ierr)
				}
			}
		}
		if initOpts.primeOnly {
			if initOpts.remove {
				c.Printf("%s\n", cliText("cli.setup.hooks.memory_removed", "path", path, "agent", agent, "count", res.Removed))
			} else {
				c.Printf("%s\n", cliText("cli.setup.hooks.memory_written", "path", path, "agent", agent, "count", res.Added))
			}
			continue
		}
		switch {
		case initOpts.remove:
			c.Printf("%s\n", cliText("cli.setup.hooks.hooks_removed", "path", res.Path, "agent", agent, "count", res.Removed))
		case res.Created:
			c.Printf("%s\n", cliText("cli.setup.hooks.hooks_created", "path", res.Path, "agent", agent, "count", res.Added))
		default:
			c.Printf("%s\n", cliText("cli.setup.hooks.hooks_merged", "agent", agent, "path", res.Path, "count", res.Added, "replaced", res.Replaced))
		}
		for _, n := range res.Notes {
			c.Printf("%s\n", cliText("cli.setup.hooks.note", "note", n))
		}
		if !initOpts.remove {
			installed = append(installed, agent)
			for _, n := range hookrelay.PostInstallNotes(agent) {
				c.Printf("%s\n", cliText("cli.setup.hooks.tip", "tip", n))
			}
		}
	}
	if global && !initOpts.remove && !initOpts.primeOnly {
		reportProjectLevelDuplicates(c, installed)
	}
	if !initOpts.remove && !initOpts.primeOnly {
		c.Printf("%s\n", cliText("cli.setup.hooks.relay_usage"))
	}
	return nil
}

// hookAgentDir is the agent's own config directory behind a hook config file path:
// .claude / .codex / the jcode home are the file's parent, omp's file sits one level
// deeper (<agent dir>/extensions/<file>).
func hookAgentDir(path string) string {
	d := filepath.Dir(path)
	if filepath.Base(d) == "extensions" {
		d = filepath.Dir(d)
	}
	return d
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// skillConventionDirs are the parallel skill-directory conventions this
// environment honours: `.claude/skills/<name>/` and `.agents/skills/<name>/`.
// `gofer init skill` (default / --global) installs into BOTH so either tool
// picks the skill up; --output narrows to a single explicit dir.
var skillConventionDirs = []string{".claude", ".agents"}

// runInitSkill installs the embedded gofer-usage/ skill tree under EACH resolved
// skills parent dir (preserving structure). Landing points: --output <dir> writes
// the single <dir>/gofer-usage/ (explicit override, no fan-out); --global writes
// ~/.claude/skills/ AND ~/.agents/skills/; otherwise the CWD's .claude/skills/
// AND .agents/skills/. It refuses to overwrite an existing skill dir unless
// --force is given (design D6), checked per target; --force overwrites all.
func runInitSkill(c *gcli.Command) error {
	parents, usedGlobal, err := skillDestParents()
	if err != nil {
		return errorx.Failf(configExitErr, "resolve skill dir: %v", err)
	}

	// -a all: only install under the convention dirs (.claude / .agents) that already
	// exist; the rest are skipped and listed. Any other -a value (or the default) keeps
	// creating every destination. --output is an explicit single dir and is never filtered.
	if strings.EqualFold(initOpts.agent, "all") && initOpts.config == "" {
		var kept []string
		for _, parent := range parents {
			conv := filepath.Dir(parent) // <root>/.claude for <root>/.claude/skills
			if dirExists(conv) {
				kept = append(kept, parent)
				continue
			}
			c.Printf("%s\n", cliText("cli.setup.skill.skip_missing_dir", "dir", conv))
		}
		if parents = kept; len(parents) == 0 {
			c.Printf("%s\n", cliText("cli.setup.skill.no_dest_dir"))
			return nil
		}
	}

	// Overwrite guard: check every target first, so a partial install never happens
	// when one destination already exists without --force.
	if !initOpts.force {
		for _, parent := range parents {
			skillDir := filepath.Join(parent, skills.SkillDirName)
			if _, statErr := os.Stat(skillDir); statErr == nil {
				return errorx.Failf(configExitErr, "skill %s already exists; use --force to overwrite", skillDir)
			} else if !os.IsNotExist(statErr) {
				return errorx.Failf(configExitErr, "stat %s: %v", skillDir, statErr)
			}
		}
	}

	for _, parent := range parents {
		written, wErr := skills.InstallTo(parent)
		if wErr != nil {
			return errorx.Failf(configExitErr, "write skill: %v", wErr)
		}
		skillDir := filepath.Join(parent, skills.SkillDirName)
		abs := skillDir
		if a, absErr := filepath.Abs(skillDir); absErr == nil {
			abs = a
		}
		c.Printf("%s\n", cliText("cli.setup.skill.installed", "path", abs, "count", len(written)))
		for _, f := range written {
			c.Printf("  %s\n", f)
		}
	}
	if usedGlobal {
		c.Printf("%s\n", cliText("cli.setup.skill.global_hint"))
	}
	return nil
}

// skillDestParents resolves the skills PARENT dir(s) for `gofer init skill` (the
// gofer-usage/ dir is created underneath each). --output wins and yields a SINGLE
// dir (explicit override, no fan-out); else --global → <home>/{.claude,.agents}/skills;
// else the CWD's {.claude,.agents}/skills (project-level). usedGlobal drives the
// trailing global hint.
func skillDestParents() (parents []string, usedGlobal bool, err error) {
	if initOpts.config != "" {
		return []string{initOpts.config}, false, nil
	}
	root, err := os.Getwd()
	usedGlobal = initOpts.global
	if usedGlobal {
		root, err = os.UserHomeDir()
	}
	if err != nil {
		return nil, false, err
	}
	for _, conv := range skillConventionDirs {
		parents = append(parents, filepath.Join(root, conv, "skills"))
	}
	return parents, usedGlobal, nil
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
					c.StrOpt(&configOpts.editKey, "key", "k", "", "open the file holding this top-level section (e.g. agents) when the config is split with include")
				},
				Func: runConfigEdit,
			},
			{
				Name: "split",
				Desc: "Move top-level sections (default: agents, projects) of the server config into their own files and include them",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.StrOpt(&configOpts.splitKeys, "keys", "", strings.Join(config.DefaultSplitKeys, ","), "comma-separated top-level sections to move")
					c.StrOpt(&configOpts.splitDir, "dir", "", "config.d", "directory for the section files, relative to the main config file")
					c.BoolOpt(&configOpts.dryRun, "dry-run", "", false, "print what would change without writing")
				},
				Func: runConfigSplit,
			},
			{
				Name: "lint",
				Desc: "Report config keys that can be deleted without changing the effective config (and agents that could use extends: builtin)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.AddArg("target", "what to lint: server (default, config.yaml and its include files) | worker (worker.yaml)", false)
					c.BoolOpt(&configOpts.fix, "fix", "", false, "delete the removable keys (files are backed up; the result is checked to load the same)")
				},
				Func: runConfigLint,
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

// configOpts holds the `config` subcommands' flags.
var configOpts struct {
	editKey   string
	splitKeys string
	splitDir  string
	dryRun    bool
	fix       bool
}

// runConfigLint prints configlint's report, or applies its removals with --fix.
func runConfigLint(c *gcli.Command, _ []string) error {
	if config.IsClientRunMode() {
		return clientModeRefusal("config lint reads a local config")
	}
	kind := configlint.TargetServer
	if a := c.Arg("target"); a != nil && a.String() != "" {
		kind = configlint.Target(strings.ToLower(a.String()))
	}
	var path string
	var err error
	switch kind {
	case configlint.TargetServer:
		path, err = config.Resolve(config.InputCfgFile)
	case configlint.TargetWorker:
		path = config.InputCfgFile
		if path == "" {
			path, err = config.UserWorkerConfigPath()
		}
	default:
		return errorx.Failf(configExitErr, "unknown lint target %q (use: server | worker)", kind)
	}
	if err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}
	if path == "" {
		return errorx.Failf(configExitErr, "no config file found")
	}
	host, _ := os.Hostname()
	c.Printf("judged against this machine (%s): built-in defaults of this gofer build\n", dashIfEmpty(host))
	if configOpts.fix {
		res, err := configlint.Fix(path, kind)
		if err != nil {
			return errorx.Failf(configExitErr, "%v", err)
		}
		lines := 0
		for _, f := range res.Removed {
			c.Printf("removed %s:%d %s\n", relToConfig(path, f.File), f.Line, f.Path)
			lines += f.Lines
		}
		if len(res.Removed) == 0 {
			c.Println("nothing to remove")
			return nil
		}
		c.Printf("removed %d keys (%d lines); backups: %s\n", len(res.Removed), lines, strings.Join(res.Backups, ", "))
		if kind == configlint.TargetWorker {
			c.Println("the effective config is unchanged; a running worker picks it up on its next reload (gofer worker reload <id>)")
		} else {
			c.Println("the effective config is unchanged; a running server picks it up on its next reload (gofer serve reload)")
		}
		return nil
	}
	r, err := configlint.Lint(path, kind)
	if err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}
	extends, unknown := 0, 0
	for _, f := range r.Findings {
		c.Printf("%s:%d  %s  %s\n", relToConfig(path, f.File), f.Line, f.Path, f.Reason)
		if f.Kind == configlint.KindUnknown {
			unknown++
		}
		if f.Kind == configlint.KindExtends {
			extends++
			for _, l := range strings.Split(f.Suggestion, "\n") {
				c.Printf("    | %s\n", l)
			}
		}
	}
	keys, lines := r.Removable()
	c.Printf("summary: %d keys can be removed (%d lines, gofer config lint --fix), %d agents can use extends: builtin, %d unknown keys\n", keys, lines, extends, unknown)
	return nil
}

// relToConfig shows file relative to the main config's directory when it is inside it.
func relToConfig(mainPath, file string) string {
	if rel, err := filepath.Rel(filepath.Dir(mainPath), file); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return file
}

// runConfigSplit moves sections of the server config into their own files
// (config.SplitConfig does the work and the equivalence check).
func runConfigSplit(c *gcli.Command, _ []string) error {
	if config.IsClientRunMode() {
		return clientModeRefusal("config split rewrites a local config")
	}
	path, err := config.Resolve(config.InputCfgFile)
	if err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}
	if path == "" {
		return errorx.Failf(configExitErr, "no config file found")
	}
	var keys []string
	for _, k := range strings.Split(configOpts.splitKeys, ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	res, err := config.SplitConfig(path, keys, configOpts.splitDir, configOpts.dryRun)
	if err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}
	sections := make([]string, 0, len(res.Files))
	for k := range res.Files {
		sections = append(sections, k)
	}
	sort.Strings(sections)
	verb := "moved"
	if configOpts.dryRun {
		verb = "would move"
	}
	for _, k := range sections {
		c.Printf("%s %s -> %s\n", verb, k, res.Files[k])
	}
	skipped := make([]string, 0, len(res.Skipped))
	for k := range res.Skipped {
		skipped = append(skipped, k)
	}
	sort.Strings(skipped)
	for _, k := range skipped {
		c.Printf("skipped %s: %s\n", k, res.Skipped[k])
	}
	if configOpts.dryRun {
		c.Printf("--- %s (new text) ---\n%s", res.Main, res.NewMain)
		return nil
	}
	c.Printf("main file %s now includes %s (backup: %s)\n", res.Main, strings.Join(res.Include, ", "), res.Backup)
	c.Println("the effective config is unchanged; a running server picks the files up on its next reload (gofer serve reload)")
	return nil
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
	// Nothing to open: a client node keeps only the connection env, and the file it
	// would want to edit (its .env) is not a gofer config at all.
	if config.IsClientRunMode() {
		return clientModeRefusal("config edit opens a local config")
	}
	path, err := config.Resolve(config.InputCfgFile)
	if err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}
	if path == "" {
		return errorx.Failf(configExitErr, "no config file found; run `gofer init` (or `gofer init --global`) first")
	}

	if key := strings.TrimSpace(configOpts.editKey); key != "" {
		file, err := config.SectionFileOf(path, key)
		if err != nil {
			return errorx.Failf(configExitErr, "%v", err)
		}
		path = file
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
// shows the worker.yaml view (worker_id / hub link(s) / served projects); client
// mode shows that there is NO local config and where the node gets its connection
// from — matching how `project`/`config validate` already branch on run mode. The
// token is NEVER printed — only whether it is set (SR403) — and neither is any
// secret value.
func runConfigInfo(c *gcli.Command, _ []string) error {
	mode := config.RunMode()

	c.Println("run_mode:")
	c.Printf("  %s (%s=%s)\n", mode, config.EnvRunMode, envOrUnset(config.EnvRunMode))

	c.Println("env:")
	c.Printf("  %s=%s\n", config.EnvConfigPath, envOrUnset(config.EnvConfigPath))
	c.Printf("  %s=%s\n", config.EnvConfigDir, envOrUnset(config.EnvConfigDir))
	c.Printf("  GOFER_TOKEN set=%s\n", envSetYesNo("GOFER_TOKEN"))

	var err error
	switch mode {
	case config.RunModeClient:
		err = printClientConfigInfo(c)
	case config.RunModeWorker:
		err = printWorkerConfigInfo(c)
	default:
		err = printServerConfigInfo(c)
	}
	if err != nil {
		return err
	}

	if mode == config.RunModeClient {
		// printClientConfigInfo already reported the submit target (it IS the only
		// thing a client node has); repeating it here would just be noise.
		return nil
	}

	// Client submit target: `job`/`wf`/`mcp` connect HERE (GOFER_SERVER_ADDR/TOKEN
	// from env/.env), distinct from the serve BIND address. Relevant in both roles
	// (a worker node can still run job/wf CLI). SR403: report only whether set.
	c.Println("client (job/wf submit target):")
	c.Printf("  GOFER_SERVER_ADDR=%s\n", envOrUnset("GOFER_SERVER_ADDR"))
	c.Printf("  GOFER_SERVER_TOKEN set=%s\n", envSetYesNo("GOFER_SERVER_TOKEN"))
	return nil
}

// printClientConfigInfo prints the client-node view (GOFER_RUN_MODE=client): a
// client holds no local config at all, so there is no path to show — only the
// connection env (<config-dir>/.env) it runs on. The address comes from
// GOFER_SERVER_ADDR (or an explicit --config, which a client node may still name),
// and the token is reported as set/not-set only (SR403).
func printClientConfigInfo(c *gcli.Command) error {
	cfg, _, err := config.Load(config.InputCfgFile)
	if err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}
	addr := cfg.Server.Addr
	if env := strings.TrimSpace(os.Getenv("GOFER_SERVER_ADDR")); env != "" {
		addr = env // the env (incl. <config-dir>/.env) is a client node's normal source
	}
	dir, err := config.ConfigDir()
	if err != nil {
		dir = ""
	}
	c.Println("config path:")
	c.Println("  (no local config in client mode)")
	c.Println("settings (client):")
	c.Printf("  server.addr:  %s\n", dashIfEmpty(addr))
	c.Printf("  server token: set=%s\n", envSetYesNo("GOFER_SERVER_TOKEN"))
	c.Printf("  config dir:   %s\n", dashIfEmpty(dir))
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
	// gofer-arsj: a config split with include lists every file and its sections.
	if files := cfg.SourceFiles(); len(files) > 1 {
		bySection := cfg.SectionSources()
		c.Println("config files:")
		for _, f := range files {
			var keys []string
			for k, rel := range bySection {
				if filepath.Join(filepath.Dir(path), filepath.FromSlash(rel)) == f {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			c.Printf("  %s  [%s]\n", f, strings.Join(keys, ", "))
		}
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
	c.Println(cliText("cli.config.show.diagnostic_note"))
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
	// Both targets are LOCAL configs (config.yaml / worker.yaml); a client node has
	// neither, so there is nothing to validate here — the server owns the config.
	if config.IsClientRunMode() {
		return clientModeRefusal("config validate checks a local config")
	}
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
	// Retired sections (supervisor / presence) are ignored: advisory only.
	for _, w := range reg.Config().LoadWarnings() {
		c.Printf("[%s] config  %s\n", docStatusWarn, w)
	}
	// server.language names no language: every generated text runs in the
	// DEPRECATED default. Advisory only — the default keeps an existing install
	// behaving as it did (design 2026-10-27-i18n-design.md §3).
	if cfg := reg.Config(); cfg != nil {
		if cfg.LanguageUnset() {
			c.Printf("[%s] %s\n", docStatusWarn, i18n.LanguageDefaultNote)
		} else {
			c.Printf("[%s] server.language %s\n", docStatusOK, cfg.Language())
		}
	}
	// OBS-13: advisory only, never fails the command.
	if w := notify.DigestNoSubscriberWarning(reg.Config()); w != "" {
		c.Printf("[%s] work.digest  %s\n", docStatusWarn, w)
	}
	if w := reg.Config().MetricsExposureWarning(); w != "" {
		c.Printf("[%s] metrics  %s\n", docStatusWarn, w)
	}
	// AUTO-04: structural hook errors already failed the load above; what is left is
	// advisory — a command this machine cannot find (the serve host may differ).
	if cfg := reg.Config(); cfg != nil {
		for _, h := range cfg.Server.EventHooks {
			status, info := eventHookCheck(h)
			c.Printf("[%s] event_hook/%-12s %s\n", status, h.Name, info)
		}
		// JOB-06②: a secret whose variable is unset here fails the jobs that ask for
		// it on this machine (the server host may differ, so only a warning).
		for _, name := range sortedStringKeys(func() map[string]string {
			m := map[string]string{}
			for k := range cfg.Secrets {
				m[k] = k
			}
			return m
		}()) {
			sec := cfg.Secrets[name]
			if v, ok := os.LookupEnv(sec.Env); ok && v != "" {
				c.Printf("[%s] secret/%-14s %s -> %s\n", docStatusOK, name, sec.Env, sec.JobVar(name))
			} else {
				c.Printf("[%s] secret/%-14s %s is not set on this machine (jobs asking for it are refused here)\n", docStatusWarn, name, sec.Env)
			}
		}
		// AUTO-04b: the same check for decision hooks (points are shown too).
		for _, h := range cfg.Server.DecisionHooks {
			status, info := eventHookCheck(config.EventHookConfig{Command: h.Command, Dir: h.Dir, Enabled: h.Enabled})
			c.Printf("[%s] decision_hook/%-9s %s [%s]\n", status, h.Name, info, strings.Join(h.Points, ", "))
		}
	}
	// CFG-16: the format was checked by Load; a bin this machine cannot find is only
	// a warning (the declaration may describe another environment).
	if w := toolsBinWarning(reg.Config().Tools); w != "" {
		c.Printf("[%s] tools  %s\n", docStatusWarn, w)
	}
	// MCP-06: a secret variable this machine lacks only fails the jobs that run HERE
	// with that server bound; a worker reads its own environment.
	mcpCfg := reg.Config()
	for _, name := range sortedStringKeys(func() map[string]string {
		m := map[string]string{}
		for k := range mcpCfg.MCPServers {
			m[k] = k
		}
		return m
	}()) {
		var missing []string
		for _, env := range mcpCfg.MCPServers[name].SecretVars() {
			if _, ok := os.LookupEnv(env); !ok {
				missing = append(missing, env)
			}
		}
		if len(missing) > 0 {
			c.Printf("[%s] mcp/%-16s secret variable(s) not set on this machine: %s (jobs run here with it bound will fail; workers read their own)\n", docStatusWarn, name, strings.Join(missing, ", "))
		}
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

// eventHookCheck reports whether a hook's command[0] resolves on this machine: a
// bare name via PATH, a path (relative = under the hook's dir) via stat. A miss is a
// WARN, never a FAIL — `config validate` may run on a different machine than serve.
func eventHookCheck(h config.EventHookConfig) (string, string) {
	info := strings.Join(h.Command, " ")
	if !h.IsEnabled() {
		info += " (disabled)"
	}
	bin := h.Command[0]
	if strings.ContainsAny(bin, `/\`) {
		p := bin
		if !filepath.IsAbs(p) {
			p = filepath.Join(h.EffectiveDir(), p)
		}
		if _, err := os.Stat(p); err != nil {
			return docStatusWarn, info + " — command not found: " + p
		}
		return docStatusOK, info
	}
	if _, err := exec.LookPath(bin); err != nil {
		return docStatusWarn, info + " — command not found in PATH: " + bin
	}
	return docStatusOK, info
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

	// CFG-16: loadWorkerConfig already rejected a malformed tools block; a missing
	// bin only warns (the declaration may describe another environment).
	if w := toolsBinWarning(wc.Tools); w != "" {
		line(docStatusWarn, "tools", w)
	} else if len(wc.Tools) > 0 {
		line(docStatusOK, "tools", fmt.Sprintf("%d declared", len(wc.Tools)))
	}

	switch workerModeOf(wc) {
	case modePolicy:
		validateWorkerRoots(c, wc, line)
		validateWorkerGuards(wc, line)
		n, note := effectivePolicyProjectCount(wc)
		line(docStatusInfo, "projects",
			cliText("cli.config.validate.policy_projects", "count", n, "note", note))
	case modeEmpty:
		chk("projects", false, cliText("cli.config.validate.no_projects"))
	default: // modeLegacy
		line(docStatusWarn, "projects",
			cliText("cli.config.validate.legacy_projects_deprecated", "doc", migrationDoc))
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

// toolsBinWarning describes the declared tools whose executable is not found on this
// machine's PATH, or "" when every one resolves (or none is declared).
func toolsBinWarning(tools []config.Tool) string {
	missing := config.MissingToolBins(tools)
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d declared tools not found on this machine: %s",
		len(missing), len(tools), strings.Join(missing, ", "))
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
		if !project.AllowsLocalRunner(p.AllowedRunners) {
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
		line(docStatusFail, "roots", cliText("cli.config.validate.roots_empty_policy"))
		return
	}
	seen := map[string]bool{}
	for i, r := range wc.Roots {
		name := fmt.Sprintf("roots[%d]", i)
		from := normRootPrefix(r.From)
		to := strings.TrimSpace(r.To)
		if from == "" {
			line(docStatusFail, name, cliText("cli.config.validate.root_from_empty"))
			continue
		}
		if to == "" {
			line(docStatusFail, name, cliText("cli.config.validate.root_to_empty", "from", r.From))
			continue
		}
		if seen[from] {
			line(docStatusFail, name, cliText("cli.config.validate.root_dup_from", "from", r.From))
			continue
		}
		seen[from] = true
		if fi, statErr := os.Stat(to); statErr != nil || !fi.IsDir() {
			line(docStatusFail, name, cliText("cli.config.validate.root_to_missing", "from", r.From, "to", to))
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
					cliText("cli.config.validate.roots_overlap", "longer", wc.Roots[j].From, "shorter", wc.Roots[i].From))
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
			cliText("cli.config.validate.guards_unset"))
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
		return 0, cliText("cli.config.validate.policy_cache_error", "err", err.Error())
	}
	if p == nil {
		return 0, cliText("cli.config.validate.policy_no_cache")
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

// reportProjectLevelDuplicates runs after a --global install: a project that
// already carries gofer's hooks of its own (the cwd, plus every project the
// loaded config knows) would fire the same event twice — once from the user
// level, once from the project level. gofer never deletes those on its own; it
// names each one with the exact command that removes it.
func reportProjectLevelDuplicates(c *gcli.Command, agents []string) {
	var cands []string
	if wd, err := os.Getwd(); err == nil {
		cands = append(cands, wd)
	}
	if cfg, _, err := config.Load(config.InputCfgFile); err == nil && cfg != nil {
		for _, p := range cfg.Projects {
			for _, r := range []string{p.HostPath, p.ContainerPath} {
				if strings.TrimSpace(r) != "" {
					cands = append(cands, r)
				}
			}
		}
	}
	home, _ := os.UserHomeDir()
	sorted := dedupeDirs(cands, runtime.GOOS == "windows")
	sort.Strings(sorted)
	found := false
	for _, root := range sorted {
		if home != "" && dirKey(root, runtime.GOOS == "windows") == dirKey(home, runtime.GOOS == "windows") {
			continue // the user level itself, not a project
		}
		for _, agent := range agents {
			if agent == hookrelay.AgentJcode || !hookrelay.HasRelayHooks(agent, root) {
				continue
			}
			if !found {
				c.Printf("%s\n", cliText("cli.setup.hooks.duplicates_head"))
				found = true
			}
			c.Printf("  gofer init hooks --remove --agent %s -o %s\n", agent, root)
		}
	}
}

// dirKey is the comparison key of a directory: separators unified to '/', cleaned,
// and lower-cased when the filesystem is case-insensitive (Windows). `D:/x`,
// `D:\x` and `d:\X\` all map to one key.
func dirKey(dir string, caseInsensitive bool) string {
	k := path.Clean(strings.ReplaceAll(strings.TrimSpace(dir), `\`, "/"))
	if caseInsensitive {
		k = strings.ToLower(k)
	}
	return k
}

// dedupeDirs drops directories that name the same place by dirKey, keeping the
// first spelling seen (so the printed command uses a path the user wrote).
func dedupeDirs(dirs []string, caseInsensitive bool) []string {
	seen := make(map[string]struct{}, len(dirs))
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		k := dirKey(d, caseInsensitive)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, filepath.Clean(d))
	}
	return out
}
