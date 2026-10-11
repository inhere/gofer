package commands

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// agentMCPOpts holds the flags of the `agent mcp` subcommands (MCP-06). The group sits
// under `agent` because `gofer mcp` is gofer's own MCP server.
var agentMCPOpts = struct {
	local bool
}{}

// newAgentMCPCmd builds `agent mcp ls|show` over the MCP server registry. Dual mode
// like `agent skill`: a client node reads the server's registry over HTTP, a server
// box (or --local) reads its own config.
func newAgentMCPCmd() *gcli.Command {
	bind := func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.BoolOpt(&agentMCPOpts.local, "local", "", false, "read the LOCAL config's registry even in client mode")
	}
	return &gcli.Command{
		Name: "mcp",
		Desc: "Show the MCP server registry (mcp_servers) and where each entry is bound",
		Subs: []*gcli.Command{
			{
				Name:    "list",
				Desc:    "List registry MCP servers with their type, bindings and secret readiness",
				Aliases: []string{"ls"},
				Config:  bind,
				Func:    runAgentMCPList,
			},
			{
				Name: "show",
				Desc: "Show one registry MCP server: declaration, secret variables (names only) and bindings",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("name", "MCP server name", true)
				},
				Func: runAgentMCPShow,
			},
		},
	}
}

// loadMCPInfos reads the registry from the server (remote) or the local config.
func loadMCPInfos() ([]client.MCPServerInfo, string, error) {
	if ch := useServerAPI(agentMCPOpts.local); ch.remote {
		cli, err := ch.client()
		if err != nil {
			return nil, "", err
		}
		list, err := cli.MCPServers()
		if err != nil {
			return nil, "", ch.wrap(err)
		}
		return list, "server", nil
	}
	cfg, _, err := config.Load(config.InputCfgFile)
	if err != nil {
		return nil, "", err
	}
	names := make([]string, 0, len(cfg.MCPServers))
	for n := range cfg.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]client.MCPServerInfo, 0, len(names))
	for _, n := range names {
		out = append(out, localMCPInfo(cfg, n))
	}
	return out, "this machine", nil
}

func localMCPInfo(cfg *config.Config, name string) client.MCPServerInfo {
	spec := cfg.MCPServers[name]
	info := client.MCPServerInfo{Name: name, Enabled: spec.IsEnabled(), Spec: spec}
	add := func(usage, env string) {
		_, set := os.LookupEnv(env)
		info.Secrets = append(info.Secrets, struct {
			Env   string `json:"env"`
			Set   bool   `json:"set"`
			Usage string `json:"usage"`
		}{env, set, usage})
	}
	for _, k := range sortedStringKeys(spec.SecretEnv) {
		add("env:"+k, spec.SecretEnv[k])
	}
	for _, k := range sortedStringKeys(spec.SecretHeaders) {
		add("header:"+k, spec.SecretHeaders[k])
	}
	info.Bound.Server, info.Bound.Agents, info.Bound.Projects = cfg.MCPBoundBy(name)
	return info
}

func sortedStringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func mcpBindingText(info client.MCPServerInfo) string {
	var parts []string
	if info.Bound.Server {
		parts = append(parts, "server")
	}
	for _, a := range info.Bound.Agents {
		parts = append(parts, "agent:"+a)
	}
	for _, p := range info.Bound.Projects {
		parts = append(parts, "project:"+p)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

func mcpSecretsText(info client.MCPServerInfo) string {
	if len(info.Secrets) == 0 {
		return "-"
	}
	missing := 0
	for _, s := range info.Secrets {
		if !s.Set {
			missing++
		}
	}
	if missing == 0 {
		return fmt.Sprintf("%d set", len(info.Secrets))
	}
	return fmt.Sprintf("%d/%d missing", missing, len(info.Secrets))
}

func runAgentMCPList(c *gcli.Command, _ []string) error {
	list, where, err := loadMCPInfos()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		c.Println("no MCP servers declared (add them under mcp_servers in the server config)")
		return nil
	}
	c.Printf("%-20s %-6s %-8s %-18s %s\n", "NAME", "TYPE", "ENABLED", "SECRETS("+where+")", "BOUND")
	for _, info := range list {
		c.Printf("%-20s %-6s %-8v %-18s %s\n", info.Name, info.Spec.EffectiveType(), info.Enabled, mcpSecretsText(info), mcpBindingText(info))
	}
	return nil
}

func runAgentMCPShow(c *gcli.Command, _ []string) error {
	name := c.Arg("name").String()
	list, where, err := loadMCPInfos()
	if err != nil {
		return err
	}
	for _, info := range list {
		if info.Name != name {
			continue
		}
		s := info.Spec
		c.Printf("name:     %s\n", info.Name)
		if s.Desc != "" {
			c.Printf("desc:     %s\n", s.Desc)
		}
		c.Printf("type:     %s\n", s.EffectiveType())
		c.Printf("enabled:  %v\n", info.Enabled)
		if s.Command != "" {
			c.Printf("command:  %s %s\n", s.Command, strings.Join(s.Args, " "))
		}
		if s.URL != "" {
			c.Printf("url:      %s\n", s.URL)
		}
		for _, k := range sortedStringKeys(s.Env) {
			c.Printf("env:      %s=%s\n", k, s.Env[k])
		}
		for _, k := range sortedStringKeys(s.Headers) {
			c.Printf("header:   %s: %s\n", k, s.Headers[k])
		}
		for _, sec := range info.Secrets {
			state := "set"
			if !sec.Set {
				state = "MISSING"
			}
			c.Printf("secret:   %s <- $%s (%s on %s)\n", sec.Usage, sec.Env, state, where)
		}
		if len(s.Tools) > 0 {
			c.Printf("tools:    %s\n", strings.Join(s.Tools, ", "))
		}
		c.Printf("bound:    %s\n", mcpBindingText(info))
		return nil
	}
	return fmt.Errorf("MCP server %q is not declared", name)
}
