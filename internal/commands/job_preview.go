package commands

import (
	"fmt"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

func previewClient() (*client.Client, error) {
	return newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
}
func newJobPreviewOpenCmd() *gcli.Command {
	var port int
	var ttl string
	return &gcli.Command{Name: "open", Desc: "Open a local preview lease", Config: func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.IntOpt(&port, "port", "p", 0, "local application port")
		c.StrOpt(&ttl, "ttl", "", "30m", "lease duration")
		c.AddArg("id", "job id", true)
	}, Func: func(c *gcli.Command, _ []string) error {
		if port < 1 || port > 65535 {
			return fmt.Errorf("--port must be between 1 and 65535")
		}
		d, e := time.ParseDuration(ttl)
		if e != nil || d <= 0 {
			return fmt.Errorf("invalid --ttl %q", ttl)
		}
		cli, e := previewClient()
		if e != nil {
			return e
		}
		v, e := cli.CreateJobPreview(argID(c), port, int(d/time.Second))
		if e != nil {
			return e
		}
		c.Printf("%s\n", v.URL)
		return nil
	}}
}
func newJobPreviewListCmd() *gcli.Command {
	return &gcli.Command{Name: "ls", Aliases: []string{"list"}, Desc: "List job preview leases", Config: func(c *gcli.Command) { bindConfigFlag(c); bindServerFlags(c); c.AddArg("id", "job id", true) }, Func: func(c *gcli.Command, _ []string) error {
		cli, e := previewClient()
		if e != nil {
			return e
		}
		vs, e := cli.ListJobPreviews(argID(c))
		if e != nil {
			return e
		}
		for _, v := range vs {
			c.Printf("%s\t%d\t%s\t%s\n", v.ID, v.Port, v.State, time.Unix(v.ExpiresAt, 0).Format(time.RFC3339))
		}
		return nil
	}}
}
func newJobPreviewCloseCmd() *gcli.Command {
	return &gcli.Command{Name: "close", Aliases: []string{"revoke"}, Desc: "Close a job preview lease", Config: func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.AddArg("job_id", "job id", true)
		c.AddArg("preview_id", "preview id", true)
	}, Func: func(c *gcli.Command, _ []string) error {
		cli, e := previewClient()
		if e != nil {
			return e
		}
		v, e := cli.CloseJobPreview(argString(c, "job_id"), argString(c, "preview_id"))
		if e != nil {
			return e
		}
		c.Printf("%s\t%s\n", v.ID, v.State)
		return nil
	}}
}
