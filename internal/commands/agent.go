package commands

import (
	"fmt"
	"sort"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

// agentOpts holds the --config flag shared by agent list/detect/show.
var agentOpts = struct {
	listConfig   string
	detectConfig string
	showConfig   string
}{}

// NewAgentCmd builds the `agent` command group (list/detect/show). P3 logic.
func NewAgentCmd() *gcli.Command {
	return &gcli.Command{
		Name: "agent",
		Desc: "Inspect configured agents",
		Subs: []*gcli.Command{
			{
				Name: "list",
				Desc: "List configured agents",
				Config: func(c *gcli.Command) {
					c.StrOpt(&agentOpts.listConfig, "config", "c", "", "path to the bridge config file")
				},
				Func: runAgentList,
			},
			{
				Name: "detect",
				Desc: "Run detect commands and report agent availability",
				Config: func(c *gcli.Command) {
					c.StrOpt(&agentOpts.detectConfig, "config", "c", "", "path to the bridge config file")
				},
				Func: runAgentDetect,
			},
			{
				Name: "show",
				Desc: "Show an agent's configuration",
				Config: func(c *gcli.Command) {
					c.StrOpt(&agentOpts.showConfig, "config", "c", "", "path to the bridge config file")
					c.AddArg("key", "agent key", true)
				},
				Func: runAgentShow,
			},
		},
	}
}

// loadAgentRegistry loads the config and wraps it in an agent.Registry.
func loadAgentRegistry(explicitPath string) (*agent.Registry, error) {
	cfg, _, err := config.Load(explicitPath)
	if err != nil {
		return nil, err
	}
	return agent.NewRegistry(cfg), nil
}

func runAgentList(c *gcli.Command, _ []string) error {
	reg, err := loadAgentRegistry(agentOpts.listConfig)
	if err != nil {
		return err
	}
	names := reg.Names()
	if len(names) == 0 {
		c.Println("(no agents configured)")
		return nil
	}
	for _, name := range names {
		ac, _ := reg.Get(name)
		command := ac.Command
		if command == "" {
			command = "-"
		}
		c.Printf("%-12s type=%-10s command=%s\n", name, ac.Type, command)
	}
	return nil
}

// runAgentDetect probes every agent. Unavailable CLIs are reported but the
// command still exits 0 (plan §9-P3): detection never fails the process.
func runAgentDetect(c *gcli.Command, _ []string) error {
	reg, err := loadAgentRegistry(agentOpts.detectConfig)
	if err != nil {
		return err
	}
	for _, name := range reg.Names() {
		res := reg.Detect(name)
		if res.Available {
			c.Printf("%-12s available   version=%s\n", name, res.Version)
		} else {
			c.Printf("%-12s unavailable error=%s\n", name, res.Error)
		}
	}
	return nil
}

func runAgentShow(c *gcli.Command, _ []string) error {
	key := argKey(c)
	if key == "" {
		return fmt.Errorf("agent show requires a <key> argument")
	}
	reg, err := loadAgentRegistry(agentOpts.showConfig)
	if err != nil {
		return err
	}
	ac, ok := reg.Get(key)
	if !ok {
		return fmt.Errorf("unknown agent %q", key)
	}
	c.Printf("key:           %s\n", key)
	c.Printf("type:          %s\n", ac.Type)
	c.Printf("command:       %s\n", ac.Command)
	c.Printf("args:          %v\n", ac.Args)
	c.Printf("allow_raw_cmd: %v\n", ac.AllowRawCmd)
	if len(ac.Env) > 0 {
		keys := make([]string, 0, len(ac.Env))
		for k := range ac.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		c.Println("env:")
		for _, k := range keys {
			c.Printf("  %s=%s\n", k, ac.Env[k])
		}
	} else {
		c.Printf("env:           (none)\n")
	}
	c.Printf("detect:        command=%s args=%v\n", ac.Detect.Command, ac.Detect.Args)
	return nil
}
