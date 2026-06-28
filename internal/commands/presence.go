package commands

import (
	"fmt"

	"github.com/gookit/cliui/show/table"
	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
)

// presenceLsOpts / presenceSendOpts / presenceInboxOpts hold the `presence`
// subcommand flags. They mirror the client method parameters 1:1.
var (
	presenceLsOpts   = struct{ role, project string }{}
	presenceSendOpts = struct {
		from, to, kind, body, ref string
	}{}
	presenceInboxOpts = struct {
		token string
		peek  bool
	}{}
)

// NewPresenceCmd builds the `presence` command group: inspect the driver-agent
// registry and drive the inbox from the CLI (E36). It is the DRIVER-agent surface
// — distinct from `agent` (which inspects the configured JOB-agents claude/codex/
// exec). The design's core二分: driver agents collaborate via the mailbox, job
// agents are work units. Wraps the server's /v1/agents/* + /v1/messages API.
func NewPresenceCmd() *gcli.Command {
	return &gcli.Command{
		Name:    "presence",
		Desc:    "Inspect the driver-agent registry and inbox (E36)",
		Aliases: []string{"driver"},
		Subs: []*gcli.Command{
			{
				Name:    "list",
				Aliases: []string{"ls"},
				Desc:    "List online driver agents (presence registry)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&presenceLsOpts.role, "role", "", "", "filter by role")
					c.StrOpt(&presenceLsOpts.project, "project", "p", "", "filter by project key")
				},
				Func: runPresenceList,
			},
			{
				Name: "send",
				Desc: "Send a message to an agent (agent_id | role:<name> | broadcast)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&presenceSendOpts.from, "from", "", "system", "sender agent_id (provenance)")
					c.StrOpt(&presenceSendOpts.to, "to", "", "", "recipient: agent_id | role:<name> | broadcast (required)")
					c.StrOpt(&presenceSendOpts.kind, "kind", "", "note", "message kind: task|note|answer|escalation")
					c.StrOpt(&presenceSendOpts.ref, "ref", "", "", "optional reference (e.g. job:<id>)")
					c.AddArg("body", "message body", true)
				},
				Func: runPresenceSend,
			},
			{
				Name: "inbox",
				Desc: "Poll an agent's inbox (requires its agent_token)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&presenceInboxOpts.token, "agent-token", "", "", "the agent's agent_token (required)")
					c.BoolOpt(&presenceInboxOpts.peek, "peek", "", false, "peek without consuming (do not mark read)")
					c.AddArg("id", "agent id", true)
				},
				Func: runPresenceInbox,
			},
		},
	}
}

// runPresenceList prints the presence registry as a table (cliui aligns CJK).
func runPresenceList(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	agents, err := cli.ListPresence(presenceLsOpts.role, presenceLsOpts.project)
	if err != nil {
		return err
	}
	if len(agents) == 0 {
		c.Println("no agents registered")
		return nil
	}
	tb := table.New("", table.WithColMaxWidth(30))
	tb.SetHeads("AGENT_ID", "NAME", "ROLE", "PROJECT", "STATUS", "CLIENT", "LAST_SEEN")
	for _, a := range agents {
		tb.AddRow(a.AgentID, a.Name, a.Role, a.ProjectKey, a.Status, a.Client, formatStarted(a.LastSeenAt))
	}
	c.Print(tb.Render())
	return nil
}

// runPresenceSend posts a message and reports the fan-out delivered count.
func runPresenceSend(c *gcli.Command, _ []string) error {
	if presenceSendOpts.to == "" {
		return fmt.Errorf("presence send requires --to <agent_id|role:<name>|broadcast>")
	}
	body := ""
	if a := c.Arg("body"); a != nil {
		body = a.String()
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	n, err := cli.PostMessage(presenceSendOpts.from, presenceSendOpts.to, presenceSendOpts.kind, body, presenceSendOpts.ref)
	if err != nil {
		return err
	}
	c.Printf("delivered to %d recipient(s)\n", n)
	return nil
}

// runPresenceInbox polls an agent's inbox (consuming unless --peek).
func runPresenceInbox(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("presence inbox requires an <id> argument")
	}
	if presenceInboxOpts.token == "" {
		return fmt.Errorf("presence inbox requires --agent-token <agent_token>")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	msgs, err := cli.PollInbox(id, presenceInboxOpts.token, !presenceInboxOpts.peek)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		c.Println("inbox empty")
		return nil
	}
	tb := table.New("", table.WithColMaxWidth(40))
	tb.SetHeads("FROM", "KIND", "BODY", "REF", "TO_SPEC", "CREATED")
	for _, m := range msgs {
		tb.AddRow(m.FromAgent, m.Kind, m.Body, m.Ref, m.ToSpec, formatStarted(m.CreatedAt))
	}
	c.Print(tb.Render())
	return nil
}
