package commands

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/bgagent"
	"github.com/inhere/gofer/internal/config"
)

// tool_bgagent.go implements `gofer tool bg-agent ls|reset` (G033: a small operator
// utility, so it lives under `gofer tool`). ls reads the server-wide status of the
// background agents (settings.read); reset marks a kind's active sessions so the next run
// opens a fresh one (settings.write). It never edits configuration and stops nothing.

var toolBgAgentLsOpts = struct {
	jsonOut bool
}{}

// NewToolBgAgentCmd builds the `tool bg-agent` group (registered by NewToolCmd).
func NewToolBgAgentCmd() *gcli.Command {
	ls := &gcli.Command{
		Name:    "ls",
		Aliases: []string{"list", "status"},
		Desc:    cliText("cli.tool.bgagent.ls.desc"),
		Help:    cliText("cli.tool.bgagent.ls.help"),
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.BoolOpt(&toolBgAgentLsOpts.jsonOut, "json", "", false, cliText("cli.tool.bgagent.ls.json_flag"))
		},
		Func: runToolBgAgentLs,
	}
	reset := &gcli.Command{
		Name: "reset",
		Desc: cliText("cli.tool.bgagent.reset.desc"),
		Help: cliText("cli.tool.bgagent.reset.help"),
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("kind", cliText("cli.tool.bgagent.kind_arg"), true)
		},
		Func: runToolBgAgentReset,
	}
	return &gcli.Command{
		Name: "bg-agent",
		Desc: cliText("cli.tool.bgagent.desc"),
		Subs: []*gcli.Command{ls, reset},
	}
}

func runToolBgAgentLs(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return toolFail("%v", err)
	}
	view, err := cli.BackgroundAgents()
	if err != nil {
		return toolFail("%v", err)
	}
	if toolBgAgentLsOpts.jsonOut {
		raw, err := json.MarshalIndent(view, "", "  ")
		if err != nil {
			return toolFail("%v", err)
		}
		c.Println(string(raw))
		return nil
	}
	return printBgAgentView(c, view)
}

func runToolBgAgentReset(c *gcli.Command, args []string) error {
	kind := strings.TrimSpace(toolArg(c, args, 0, "kind"))
	if kind == "" {
		return toolFail("%s", cliText("cli.tool.bgagent.kind_required"))
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return toolFail("%v", err)
	}
	res, err := cli.ResetBackgroundAgent(kind)
	if err != nil {
		return toolFail("%v", err)
	}
	c.Println(cliText("cli.tool.bgagent.reset.done", "kind", res.Kind.Kind, "count", res.Reset))
	return nil
}

// printBgAgentView renders one summary row per kind and one line per active session.
func printBgAgentView(c *gcli.Command, view bgagent.View) error {
	var table strings.Builder
	tw := tabwriter.NewWriter(&table, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, strings.ReplaceAll(cliText("cli.tool.bgagent.ls.header"), ",", "\t"))
	for _, k := range view.Kinds {
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%.4f\t%s\n", k.Kind, len(k.Sessions), k.Today.Runs, k.Today.Blocked,
			k.Today.TotalTokens, k.Today.CostUSD, bgAgentLastText(k.Today))
	}
	if err := tw.Flush(); err != nil {
		return toolFail("%v", err)
	}
	c.Print(table.String())
	for _, k := range view.Kinds {
		for _, s := range k.Sessions {
			c.Println(cliText("cli.tool.bgagent.ls.session", "kind", k.Kind, "id", s.ID, "project", dashIfEmpty(s.Project),
				"agent", dashIfEmpty(s.Agent), "runs", s.Runs, "context", bgAgentContextText(s)))
		}
	}
	return nil
}

func bgAgentLastText(t bgagent.TodayView) string {
	if t.LastAt == 0 && t.LastStatus == "" {
		return "-"
	}
	out := t.LastStatus
	if t.LastAt > 0 {
		out = time.Unix(t.LastAt, 0).Format("15:04:05") + " " + out
	}
	if t.LastError != "" {
		out += " " + t.LastError
	}
	return strings.TrimSpace(out)
}

// bgAgentContextText is the context fill, or "unknown" (never 0%) when the agent reported none.
func bgAgentContextText(s bgagent.SessionView) string {
	if s.ContextRatio == nil {
		return cliText("cli.tool.bgagent.context_unknown")
	}
	return fmt.Sprintf("%.0f%%", *s.ContextRatio*100)
}
