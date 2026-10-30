package commands

import (
	"errors"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

var _ = client.Client{}

// NewRefsCmd implements `gofer tool refs <id>`; kind is inferred from the id prefix.
func NewRefsCmd() *gcli.Command {
	return &gcli.Command{
		Name:     "refs",
		Desc:     cliText("cli.refs.desc"),
		Help:     cliText("cli.refs.help"),
		Examples: cliText("cli.refs.examples"),
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("id", cliText("cli.refs.id_arg"), true)
		},
		Func: runRefs,
	}
}

func refKind(id string) string {
	for _, prefix := range []string{"gofer-", "job-", "plan-", "todo-", "wf-"} {
		if strings.HasPrefix(strings.ToLower(id), prefix) {
			if prefix == "gofer-" {
				return "issue"
			}
			return strings.TrimSuffix(prefix, "-")
		}
	}
	return ""
}

func runRefs(c *gcli.Command, _ []string) error {
	id := argID(c)
	kind := refKind(id)
	if kind == "" {
		return errors.New(cliText("cli.refs.bad_id"))
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	rows, err := cli.ListRefs(kind, id)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		c.Printf("%s\n", cliText("cli.refs.none", "id", id))
		return nil
	}
	for _, row := range rows {
		c.Printf("%s %s (%s) field=%s", row.SourceKind, row.SourceID, row.SourceTitle, row.Field)
		c.Println()
	}
	return nil
}

var _ *client.Client
