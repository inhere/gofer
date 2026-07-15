package commands

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/worker"
)

// projectAddOpts holds `project add` flags. The config path is the app-level
// global -c (config.InputCfgFile), not a per-command flag (P1).
var projectAddOpts = struct {
	hostPath       string
	containerPath  string
	exchangeSubdir string
	resultSubdir   string
	defaultAgent   string
	allowAgents    gcli.Strings // repeatable: --allow-agent
	allowRunners   gcli.Strings // repeatable: --allow-runner
	allowExec      bool
	force          bool
	interactive    bool // -i: prompt for fields, defaulting the project to the cwd
}{}

// projectListOpts holds `project list` flags. --server/--token are bound via the
// shared bindServerFlags (jobConnOpts), used only when --remote is set.
var projectListOpts = struct {
	remote bool
}{}

// NewProjectCmd builds the `project` command group with sub commands
// list/show/add/remove/validate.
func NewProjectCmd() *gcli.Command {
	return &gcli.Command{
		Name:    "project",
		Desc:    "Manage registered projects",
		Aliases: []string{"p", "proj"},
		Subs: []*gcli.Command{
			{
				Name:    "list",
				Desc:    "List projects: local config by GOFER_RUN_MODE (server→config.yaml / worker→worker.yaml), or --remote for the server's live projects",
				Aliases: []string{"ls"},
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c) // for --remote (shares jobConnOpts + GOFER_SERVER_* env)
					c.BoolOpt(&projectListOpts.remote, "remote", "", false, "list the server's live projects via API (GET /v1/meta) instead of local config")
				},
				Func: runProjectList,
			},
			{
				Name: "show",
				Desc: "Show a project's details",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.AddArg("key", "project key", true)
				},
				Func: runProjectShow,
			},
			{
				Name: "add",
				Desc: "Register a new project",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.StrOpt(&projectAddOpts.hostPath, "host-path", "", "", "absolute host path of the project (required)")
					c.StrOpt(&projectAddOpts.containerPath, "container-path", "", "", "container mount path of the project")
					c.StrOpt(&projectAddOpts.exchangeSubdir, "exchange-subdir", "", "tmp", "data exchange subdir under the project")
					c.StrOpt(&projectAddOpts.resultSubdir, "result-subdir", "", "gofer", "result subdir under the exchange subdir")
					c.StrOpt(&projectAddOpts.defaultAgent, "default-agent", "", "", "default agent for this project")
					c.VarOpt(&projectAddOpts.allowAgents, "allow-agent", "", "allowed agent (repeatable)")
					c.VarOpt(&projectAddOpts.allowRunners, "allow-runner", "", "allowed runner (repeatable)")
					c.BoolOpt(&projectAddOpts.allowExec, "allow-exec", "", false, "allow exec agent in this project")
					c.BoolOpt(&projectAddOpts.force, "force", "", false, "overwrite an existing project entry")
					c.BoolOpt(&projectAddOpts.interactive, "interactive", "i", false, "interactively add the current directory (prompts for key/paths/agents)")
					// key is optional at the gcli level so `add -i` can prompt for it
					// (defaulting to the cwd dir name); the non-interactive path still
					// requires it (runProjectAdd errors when missing).
					c.AddArg("key", "project key (required unless -i)", false)
				},
				Func: runProjectAdd,
			},
			{
				Name:    "remove",
				Desc:    "Remove a registered project",
				Aliases: []string{"rm"},
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.AddArg("key", "project key", true)
				},
				Func: runProjectRemove,
			},
			{
				Name:    "validate",
				Aliases: []string{"check"},
				Desc:    "Validate a project's paths, agents and runners",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.AddArg("key", "project key", true)
				},
				Func: runProjectValidate,
			},
		},
	}
}

// argKey returns the required <key> positional from the gcli-bound named arg.
// key is declared via AddArg(..., true), so gcli enforces presence; this only
// reads the bound value.
func argKey(c *gcli.Command) string {
	if c != nil {
		if a := c.Arg("key"); a != nil {
			return a.String()
		}
	}
	return ""
}

// loadRegistry loads the config (lookup chain) and wraps it in a Registry.
//
// It resolves the config through agent.Resolve for the same reason loadAgentRegistry
// does (P2 T0-C): project.Registry.Validate enumerates agents (agentDefined) to check
// default_agent / allowed_agents, so a core-less registry would report a
// template-materialized agent as "not defined" while the very same agent runs fine
// under serve. Registry.Add writes this config back, which is safe precisely because
// Resolve marks the injected keys and config.Save strips them (T0-A).
func loadRegistry(explicitPath string) (*project.Registry, error) {
	cfg, path, err := config.Load(explicitPath)
	if err != nil {
		return nil, err
	}
	cfg, _ = agent.Resolve(cfg, agent.DefaultDetector())
	return project.NewRegistry(cfg, path), nil
}

// runProjectList lists projects. Default: the LOCAL config matching the node role
// (GOFER_RUN_MODE — server→config.yaml / worker→worker.yaml). With --remote it
// queries the server's live projects via API instead (E38②).
func runProjectList(c *gcli.Command, _ []string) error {
	if projectListOpts.remote {
		return runProjectListRemote(c)
	}
	projs, src, err := localProjects()
	if err != nil {
		return err
	}
	if len(projs) == 0 {
		c.Printf("(no projects in %s)\n", src)
		return nil
	}
	keys := make([]string, 0, len(projs))
	for k := range projs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := projs[k]
		agent := p.DefaultAgent
		if agent == "" {
			agent = "-"
		}
		c.Printf("%-20s host=%s default_agent=%s\n", k, p.HostPath, agent)
	}
	return nil
}

// localProjects returns the projects from the LOCAL config file matching the node
// role: GOFER_RUN_MODE=worker → the worker's effective project set; server
// (default) → config.yaml.Projects. The second value is a short source label for
// the empty-list hint. WorkerConfig.Projects and Config.Projects are the same
// map[string]config.ProjectConfig, so rendering is identical (E38②).
//
// A worker's effective project set depends on its mode (T6-A): a LEGACY worker
// sources them VERBATIM from worker.yaml (unchanged behaviour); a POLICY worker's
// projects come from the SERVER, so the live truth is the last-known-good policy
// cache the running worker persisted (workerPolicyCachePath) — read-only, never a
// panic when the file is absent (worker not running / no policy yet).
func localProjects() (map[string]config.ProjectConfig, string, error) {
	if config.RunMode() == config.RunModeWorker {
		wc, err := loadWorkerConfig("") // <config-dir>/worker.yaml (UserWorkerConfigPath)
		if err != nil {
			return nil, "worker.yaml", err
		}
		if workerModeOf(wc) == modePolicy {
			return workerPolicyProjects(wc)
		}
		// LEGACY / EMPTY: the worker sources projects from its own worker.yaml.
		return wc.Projects, "worker.yaml (GOFER_RUN_MODE=worker)", nil
	}
	cfg, path, err := config.Load(config.InputCfgFile)
	if err != nil {
		return nil, "config.yaml", err
	}
	if path == "" {
		path = "config.yaml"
	}
	return cfg.Projects, path, nil
}

// workerPolicyProjects returns a POLICY worker's currently-effective projects,
// read from the last-known-good policy cache and projected onto the worker's roots
// (the SAME projection the running worker applies, so `project list` shows exactly
// what the worker would run). A missing/unusable cache is NOT an error: the worker
// is simply not running or has not received a policy yet, so it returns an empty
// set with an explanatory source label (the caller renders "(no projects in …)").
func workerPolicyProjects(wc *config.WorkerConfig) (map[string]config.ProjectConfig, string, error) {
	p, err := worker.ReadPolicyCacheFile(workerPolicyCachePath(wc.WorkerID), wc.WorkerID)
	if err != nil {
		// A half-written / foreign-worker cache: treat as "no cache" (never panic),
		// but say why so the operator can tell it apart from a clean empty.
		return nil, "policy 缓存不可用 (worker 未运行或尚未收到 Policy): " + err.Error(), nil
	}
	if p == nil {
		return nil, "policy 缓存不存在 (worker 未运行或尚未收到 Policy)", nil
	}
	cfg, _ := projectPolicy(wc, *p) // rejected (path_outside_roots) drop out — not effective
	return cfg.Projects, "policy 缓存 (server 下发)", nil
}

// runProjectListRemote lists the SERVER's live projects via API (--remote). It
// reuses the shared jobConnOpts (--server/--token + GOFER_SERVER_* env), so a
// worker node can inspect what the server has registered. Remote view carries no
// host_path (server-side path; the meta endpoint omits it) — it shows the
// allowlists instead.
func runProjectListRemote(c *gcli.Command) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	projs, err := cli.ListProjects()
	if err != nil {
		return err
	}
	if len(projs) == 0 {
		c.Println("no projects on server")
		return nil
	}
	for _, p := range projs {
		agent := p.DefaultAgent
		if agent == "" {
			agent = "-"
		}
		c.Printf("%-20s default_agent=%s allowed_agents=%s\n", p.Key, agent, strings.Join(p.AllowedAgents, ","))
	}
	return nil
}

func runProjectShow(c *gcli.Command, _ []string) error {
	key := argKey(c)
	if key == "" {
		return fmt.Errorf("project show requires a <key> argument")
	}
	reg, err := loadRegistry(config.InputCfgFile)
	if err != nil {
		return err
	}
	p, err := reg.Get(key)
	if err != nil {
		return err
	}
	c.Printf("key:             %s\n", key)
	c.Printf("host_path:       %s\n", p.HostPath)
	c.Printf("container_path:  %s\n", p.ContainerPath)
	c.Printf("exchange_subdir: %s\n", reg.Config().ResolvedExchangeSubdir(p))
	c.Printf("result_subdir:   %s\n", reg.Config().ResolvedResultSubdir(p))
	c.Printf("default_agent:   %s\n", p.DefaultAgent)
	c.Printf("allowed_agents:  %v\n", p.AllowedAgents)
	c.Printf("allowed_runners: %v\n", p.AllowedRunners)
	c.Printf("allow_exec:      %v\n", p.AllowExec)
	c.Printf("max_concurrent:  %d\n", p.MaxConcurrentJobs)
	return nil
}

func runProjectAdd(c *gcli.Command, _ []string) error {
	// -i: interactively register the current directory, prompting for the fields
	// (key defaults to the cwd dir name, host_path is the cwd). Non-interactive
	// behaviour below is unchanged.
	if projectAddOpts.interactive {
		return runProjectAddInteractive(c)
	}

	key := argKey(c)
	if key == "" {
		return fmt.Errorf("project add requires a <key> argument")
	}

	if projectAddOpts.hostPath == "" {
		return fmt.Errorf("--host-path is required")
	}
	hostAbs, err := filepath.Abs(projectAddOpts.hostPath)
	if err != nil {
		return fmt.Errorf("resolve host-path: %w", err)
	}
	fi, err := os.Stat(hostAbs)
	if err != nil {
		return fmt.Errorf("host-path %s: %w", hostAbs, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("host-path %s is not a directory", hostAbs)
	}

	proj := config.ProjectConfig{
		HostPath:       hostAbs,
		ContainerPath:  projectAddOpts.containerPath,
		ExchangeSubdir: projectAddOpts.exchangeSubdir,
		ResultSubdir:   projectAddOpts.resultSubdir,
		DefaultAgent:   projectAddOpts.defaultAgent,
		AllowedAgents:  []string(projectAddOpts.allowAgents),
		AllowedRunners: []string(projectAddOpts.allowRunners),
		AllowExec:      projectAddOpts.allowExec,
	}

	reg, err := loadRegistry(config.InputCfgFile)
	if err != nil {
		return err
	}
	if err := reg.Add(key, proj, projectAddOpts.force); err != nil {
		return err
	}
	c.Printf("project %q saved to %s\n", key, reg.Path())
	return nil
}

// runProjectAddInteractive registers the current working directory as a project,
// prompting for each field with a sensible default (key = cwd dir name, host_path
// = cwd abs path). It reads from os.Stdin line-by-line (a blank line accepts the
// shown default), so it works both interactively and when fed piped input.
func runProjectAddInteractive(c *gcli.Command) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	hostAbs, err := filepath.Abs(cwd)
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}

	sc := bufio.NewScanner(os.Stdin)
	ask := func(prompt, def string) string {
		if def != "" {
			c.Printf("%s [%s]: ", prompt, def)
		} else {
			c.Printf("%s: ", prompt)
		}
		if !sc.Scan() {
			return def
		}
		v := strings.TrimSpace(sc.Text())
		if v == "" {
			return def
		}
		return v
	}

	key := ask("project key", filepath.Base(hostAbs))
	if key == "" {
		return fmt.Errorf("project key is required")
	}
	c.Printf("host_path: %s\n", hostAbs)

	proj := config.ProjectConfig{
		HostPath:      hostAbs,
		ContainerPath: ask("container_path (mount path inside container, blank for none)", ""),
		DefaultAgent:  ask("default_agent (blank for none)", ""),
	}
	if agents := ask("allowed_agents (comma-separated, blank for default)", ""); agents != "" {
		proj.AllowedAgents = splitCSV(agents)
	}

	reg, err := loadRegistry(config.InputCfgFile)
	if err != nil {
		return err
	}
	if err := reg.Add(key, proj, projectAddOpts.force); err != nil {
		return err
	}
	c.Printf("project %q saved to %s\n", key, reg.Path())
	return nil
}

// splitCSV splits a comma-separated list, trimming whitespace and dropping empty
// entries (so "a, b, " → ["a","b"]).
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func runProjectRemove(c *gcli.Command, _ []string) error {
	key := argKey(c)
	if key == "" {
		return fmt.Errorf("project remove requires a <key> argument")
	}
	reg, err := loadRegistry(config.InputCfgFile)
	if err != nil {
		return err
	}
	if err := reg.Remove(key); err != nil {
		return err
	}
	c.Printf("project %q removed from %s\n", key, reg.Path())
	return nil
}

func runProjectValidate(c *gcli.Command, _ []string) error {
	key := argKey(c)
	if key == "" {
		return fmt.Errorf("project validate requires a <key> argument")
	}
	reg, err := loadRegistry(config.InputCfgFile)
	if err != nil {
		return err
	}
	results, ok, err := reg.Validate(key)
	if err != nil {
		return err
	}
	for _, res := range results {
		status := "OK  "
		if !res.OK {
			status = "FAIL"
		}
		c.Printf("[%s] %-22s %s\n", status, res.Name, res.Info)
	}
	if !ok {
		return fmt.Errorf("project %q validation failed", key)
	}
	c.Printf("project %q is valid\n", key)
	return nil
}
