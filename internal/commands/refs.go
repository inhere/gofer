package commands

import (
	"fmt"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

var _ = client.Client{}

// NewRefsCmd implements `gofer refs <id>`; kind is inferred from the id prefix.
func NewRefsCmd() *gcli.Command {
	return &gcli.Command{
		Name: "refs", Desc: "List objects that reference an issue, job, plan, todo or workflow",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("id", "destination object id", true)
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
		return fmt.Errorf("refs requires an id beginning gofer-, job-, plan-, todo- or wf-")
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
		c.Printf("no references to %s\n", id)
		return nil
	}
	for _, row := range rows {
		c.Printf("%s %s (%s) field=%s", row.SourceKind, row.SourceID, row.SourceTitle, row.Field)
		c.Println()
	}
	return nil
}

var _ *client.Client
