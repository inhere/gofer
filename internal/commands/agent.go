package commands

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

var agentListOpts struct {
	runner string
	local  bool
}

// NewAgentCmd builds the `agent` command group (list/detect/show). P3 logic.
// The config path is the app-level global -c (config.InputCfgFile), not a
// per-command flag (P1).
func NewAgentCmd() *gcli.Command {
	return &gcli.Command{
		Name: "agent",
		Desc: "Inspect configured agents",
		Subs: []*gcli.Command{
			{
				Name:    "list",
				Desc:    "List agents: the server's agents by default in client mode, else the local config's",
				Aliases: []string{"ls"},
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&agentListOpts.runner, "runner", "", "", "list source: server/local or a configured runner id (default: local config, or the server when GOFER_RUN_MODE=client)")
					c.BoolOpt(&agentListOpts.local, "local", "", false, "force the LOCAL agent registry (built-in templates) even in client mode")
				},
				Func: runAgentList,
			},
			{
				Name: "detect",
				Desc: "Run detect commands and report agent availability",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
				},
				Func: runAgentDetect,
			},
			{
				Name: "show",
				Desc: "Show an agent's configuration",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.AddArg("key", "agent key", true)
				},
				Func: runAgentShow,
			},
		},
	}
}

// loadAgentRegistry loads the config and wraps it in an agent.Registry.
//
// The `agent` command group never builds a Core, so — like every other core-less
// assembly point in this package (loadRegistry, the worker doctor) — it MUST run
// agent.Resolve itself (P2 T0-C), through the same single entry point core.Build uses.
// Skipping it is how `gofer agent list` would miss every template-materialized agent:
// serve would happily run `claude` while the operator debugging the box was told it
// did not exist. Nothing is persisted from here, and injected keys are marked on the
// config regardless, so resolving is write-safe.
func loadAgentRegistry(explicitPath string) (*agent.Registry, error) {
	cfg, _, err := config.Load(explicitPath)
	if err != nil {
		return nil, err
	}
	cfg, _ = agent.Resolve(cfg, agent.DefaultDetector())
	return agent.NewRegistry(cfg), nil
}

// runAgentList lists agents. Source precedence: an explicit --runner (server / a
// configured runner id) wins; then, on a client node (GOFER_RUN_MODE=client, no
// local config) the SERVER's agents; otherwise the local registry. --local forces
// the local registry, so a client node can still inspect the built-in templates
// (what a bare `gofer` binary ships, independent of the server).
func runAgentList(c *gcli.Command, _ []string) error {
	if source := strings.TrimSpace(agentListOpts.runner); source != "" {
		return runAgentListRemote(c, source)
	}
	if config.IsClientRunMode() && !agentListOpts.local {
		return runAgentListMeta(c)
	}
	reg, err := loadAgentRegistry(config.InputCfgFile)
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

// runAgentListMeta lists the SERVER's agents from /v1/meta (client mode default).
// /v1/meta is used rather than /v1/agents because it carries the AGT-02 capability
// bits (batch/interactive) — which decide whether an agent can be submitted as a
// plain job or with --interactive — without paying for the availability probe a
// listing does not need.
func runAgentListMeta(c *gcli.Command) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	m, err := cli.Meta()
	if err != nil {
		return err
	}
	if len(m.Agents) == 0 {
		c.Println("(no agents on server)")
		return nil
	}
	for _, a := range m.Agents {
		c.Printf("%-12s type=%-10s batch=%-5v interactive=%v\n", a.Key, a.Type, a.Batch, a.Interactive)
	}
	return nil
}

func runAgentListRemote(c *gcli.Command, source string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	if source == "server" || source == "local" {
		agents, err := cli.ListAgents()
		if err != nil {
			return err
		}
		printRemoteAgents(c, "server", agents)
		return nil
	}
	runners, err := cli.ListRunners()
	if err != nil {
		return err
	}
	for _, runner := range runners {
		if runner.Name != source {
			continue
		}
		if runner.Capabilities == nil {
			return fmt.Errorf("runner %q has no advertised agent capabilities (status=%s)", source, runner.Status)
		}
		agents := make([]client.AgentMeta, 0, len(runner.Capabilities.AgentCaps))
		for _, a := range runner.Capabilities.AgentCaps {
			available := a.Available != nil && *a.Available
			detail := a.Version
			if a.Available != nil && !*a.Available {
				detail = "unavailable"
			}
			agents = append(agents, client.AgentMeta{Name: a.Key, Type: a.Type, Available: available, Detail: detail})
		}
		printRemoteAgents(c, source, agents)
		return nil
	}
	return fmt.Errorf("unknown runner %q (use server or a configured runner id)", source)
}

func printRemoteAgents(c *gcli.Command, source string, agents []client.AgentMeta) {
	if len(agents) == 0 {
		c.Printf("(no agents on %s)\n", source)
		return
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	for _, a := range agents {
		detail := a.Detail
		if detail == "" {
			detail = "-"
		}
		c.Printf("%-12s type=%-10s available=%-5t detail=%s\n", a.Name, a.Type, a.Available, detail)
	}
}

// runAgentDetect probes every agent. Unavailable CLIs are reported but the
// command still exits 0 (plan §9-P3): detection never fails the process.
func runAgentDetect(c *gcli.Command, _ []string) error {
	reg, err := loadAgentRegistry(config.InputCfgFile)
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
	reg, err := loadAgentRegistry(config.InputCfgFile)
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
