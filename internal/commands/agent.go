package commands

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

var agentListOpts struct {
	runner string
	local  bool
}

// agentProbeOpts holds `agent probe` flags: which project to probe in (default: the
// server picks the first one admitting the agent) and the probe's deadline.
var agentProbeOpts struct {
	project string
	timeout int
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
			{
				Name: "status",
				Desc: "Show agents with availability and recent-job health (provider failures)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("key", "only this agent key", false)
				},
				Func: runAgentStatus,
			},
			{
				Name: "probe",
				Desc: "Run a probe job on an agent: one line of output, exit 0/1",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("key", "agent key", true)
					c.StrOpt(&agentProbeOpts.project, "project", "p", "", "project to run the probe in (default: the first one admitting the agent)")
					c.IntOpt(&agentProbeOpts.timeout, "timeout", "", 0, "probe timeout in seconds (0 = server default 120)")
				},
				Func: runAgentProbe,
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

// usageWindow24h is the window `agent status` reports usage for: the server's
// /v1/stats spells its windows "24h"/"7d" (httpapi.statsUsageWindows).
const usageWindow24h = "24h"

// runAgentStatus prints every agent with its availability, its recent-job health and
// its 24h usage (SUP-01 P3/P4). Availability comes from the SERVER's detect cache,
// health and usage from the server's jobs table, so this is a server-side read even
// when a local config exists (an agent's provider is only "down" as observed by the
// box that runs its jobs).
func runAgentStatus(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	agents, err := cli.ListAgents()
	if err != nil {
		return err
	}
	usage, err := cli.GetUsageStats()
	if err != nil {
		return err
	}
	win24h := usage.Windows[usageWindow24h]

	only := argKey(c)
	printed := 0
	c.Printf("%-14s %-12s %-9s %-9s %5s %4s %6s  %-19s %10s %9s\n",
		"KEY", "TYPE", "AVAILABLE", "HEALTH", "JOBS", "OK", "FAILED", "LAST_TRANSIENT", "24H_TOKENS", "24H_COST")
	for _, a := range agents {
		if only != "" && a.Name != only {
			continue
		}
		printed++
		available := "no"
		if a.Available {
			available = "yes"
		}
		// 24h 用量（SUP-01 E）：窗口内没采集到用量 → `-`（没报 ≠ 0 用量），窗口根本没算
		// （预算耗尽）同理——不拿 0 冒充。
		tokens, cost := "-", "-"
		if ua, ok := win24h.ByAgent[a.Name]; ok {
			if ua.TotalTokens > 0 {
				tokens = job.FormatTokens(ua.TotalTokens)
			}
			if ua.CostUSD > 0 {
				cost = fmt.Sprintf("$%.4f", ua.CostUSD)
			}
		}
		h := a.Health
		if h == nil {
			c.Printf("%-14s %-12s %-9s %-9s %5s %4s %6s  %-19s %10s %9s\n",
				a.Name, a.Type, available, "-", "-", "-", "-", "-", tokens, cost)
			continue
		}
		c.Printf("%-14s %-12s %-9s %-9s %5d %4d %6d  %-19s %10s %9s\n",
			a.Name, a.Type, available, h.State, h.Jobs, h.OK, h.TransientFail, probeTime(h.LastTransientAt), tokens, cost)
	}
	if only != "" && printed == 0 {
		return fmt.Errorf("unknown agent %q", only)
	}
	return nil
}

// probeTime renders a probe/health timestamp in the server's zone (bd
// h-aii-tnua), or "-" when there is none: an agent that never failed must not read
// as "failed at 1970-01-01".
func probeTime(sec int64) string {
	return fmtServerTime(sec)
}

// runAgentProbe submits a probe job (SUP-01 P3) and reports its outcome. The exit code
// follows the probe: a probe that did not come back `done` is a failing check, so this
// command is usable as a liveness gate in a script or a cron.
func runAgentProbe(c *gcli.Command, _ []string) error {
	key := argKey(c)
	if key == "" {
		return fmt.Errorf("agent probe requires a <key> argument")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	res, err := cli.ProbeAgent(key, agentProbeOpts.project, agentProbeOpts.timeout)
	if err != nil {
		return err
	}
	c.Printf("job:      %s\n", res.JobID)
	c.Printf("status:   %s (exit %d, %s)\n", res.Status, res.ExitCode, formatProbeDuration(res.DurationMs))
	if res.FirstLine != "" {
		c.Printf("output:   %s\n", res.FirstLine)
	}
	if res.Status != job.StatusDone {
		return errorx.Failf(probeExitErr, "probe of %s did not succeed: status=%s exit=%d", key, res.Status, res.ExitCode)
	}
	return nil
}

// probeExitErr is the process exit code of a failed probe: 1, so `agent probe` reads
// as a plain boolean check.
const probeExitErr = 1

// formatProbeDuration renders a probe's wall time in a form a terminal reads at a
// glance (milliseconds below a second, seconds above).
func formatProbeDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}
