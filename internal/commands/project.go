package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/project"
)

// projectAddOpts holds `project add` flags.
var projectAddOpts = struct {
	config         string
	hostPath       string
	containerPath  string
	exchangeSubdir string
	resultSubdir   string
	defaultAgent   string
	allowAgents    gcli.Strings // repeatable: --allow-agent
	allowRunners   gcli.Strings // repeatable: --allow-runner
	allowExec      bool
	force          bool
}{}

// commonProjectOpts holds the --config flag shared by list/show/remove/validate.
var commonProjectOpts = struct {
	listConfig     string
	showConfig     string
	removeConfig   string
	validateConfig string
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
				Desc:    "List configured projects",
				Aliases: []string{"ls"},
				Config: func(c *gcli.Command) {
					c.StrOpt(&commonProjectOpts.listConfig, "config", "c", "", "path to the bridge config file")
				},
				Func: runProjectList,
			},
			{
				Name: "show",
				Desc: "Show a project's details",
				Config: func(c *gcli.Command) {
					c.StrOpt(&commonProjectOpts.showConfig, "config", "c", "", "path to the bridge config file")
					c.AddArg("key", "project key", true)
				},
				Func: runProjectShow,
			},
			{
				Name: "add",
				Desc: "Register a new project",
				Config: func(c *gcli.Command) {
					c.StrOpt(&projectAddOpts.config, "config", "c", "", "path to the bridge config file")
					c.StrOpt(&projectAddOpts.hostPath, "host-path", "", "", "absolute host path of the project (required)")
					c.StrOpt(&projectAddOpts.containerPath, "container-path", "", "", "container mount path of the project")
					c.StrOpt(&projectAddOpts.exchangeSubdir, "exchange-subdir", "", "tmp", "data exchange subdir under the project")
					c.StrOpt(&projectAddOpts.resultSubdir, "result-subdir", "", "gofer", "result subdir under the exchange subdir")
					c.StrOpt(&projectAddOpts.defaultAgent, "default-agent", "", "", "default agent for this project")
					c.VarOpt(&projectAddOpts.allowAgents, "allow-agent", "", "allowed agent (repeatable)")
					c.VarOpt(&projectAddOpts.allowRunners, "allow-runner", "", "allowed runner (repeatable)")
					c.BoolOpt(&projectAddOpts.allowExec, "allow-exec", "", false, "allow exec agent in this project")
					c.BoolOpt(&projectAddOpts.force, "force", "", false, "overwrite an existing project entry")
					c.AddArg("key", "project key", true)
				},
				Func: runProjectAdd,
			},
			{
				Name:    "remove",
				Desc:    "Remove a registered project",
				Aliases: []string{"rm"},
				Config: func(c *gcli.Command) {
					c.StrOpt(&commonProjectOpts.removeConfig, "config", "c", "", "path to the bridge config file")
					c.AddArg("key", "project key", true)
				},
				Func: runProjectRemove,
			},
			{
				Name:    "validate",
				Aliases: []string{"check"},
				Desc:    "Validate a project's paths, agents and runners",
				Config: func(c *gcli.Command) {
					c.StrOpt(&commonProjectOpts.validateConfig, "config", "c", "", "path to the bridge config file")
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
func loadRegistry(explicitPath string) (*project.Registry, error) {
	cfg, path, err := config.Load(explicitPath)
	if err != nil {
		return nil, err
	}
	return project.NewRegistry(cfg, path), nil
}

func runProjectList(c *gcli.Command, _ []string) error {
	reg, err := loadRegistry(commonProjectOpts.listConfig)
	if err != nil {
		return err
	}
	keys := reg.List()
	if len(keys) == 0 {
		c.Println("(no projects registered)")
		return nil
	}
	for _, k := range keys {
		p, _ := reg.Get(k)
		agent := p.DefaultAgent
		if agent == "" {
			agent = "-"
		}
		c.Printf("%-20s host=%s default_agent=%s\n", k, p.HostPath, agent)
	}
	return nil
}

func runProjectShow(c *gcli.Command, _ []string) error {
	key := argKey(c)
	if key == "" {
		return fmt.Errorf("project show requires a <key> argument")
	}
	reg, err := loadRegistry(commonProjectOpts.showConfig)
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

	reg, err := loadRegistry(projectAddOpts.config)
	if err != nil {
		return err
	}
	if err := reg.Add(key, proj, projectAddOpts.force); err != nil {
		return err
	}
	c.Printf("project %q saved to %s\n", key, reg.Path())
	return nil
}

func runProjectRemove(c *gcli.Command, _ []string) error {
	key := argKey(c)
	if key == "" {
		return fmt.Errorf("project remove requires a <key> argument")
	}
	reg, err := loadRegistry(commonProjectOpts.removeConfig)
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
	reg, err := loadRegistry(commonProjectOpts.validateConfig)
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
