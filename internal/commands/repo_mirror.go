package commands

import (
	"fmt"
	"os"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/config"
)

var repoMirrorOpts struct {
	server string
	yes    bool
	asJSON bool
}

// newRepoMirrorCmd groups the server-side views of tracker mirrors: list them and
// remove one (a test or abandoned repository's mirror otherwise stays forever).
func newRepoMirrorCmd() *gcli.Command {
	return &gcli.Command{
		Name: "mirror", Desc: "List or remove tracker mirrors on the server",
		Subs: []*gcli.Command{
			{
				Name: "ls", Desc: "List the repositories mirrored on the server",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.StrOpt(&repoMirrorOpts.server, "server", "", "", "server URL override")
					c.BoolOpt(&repoMirrorOpts.asJSON, "json", "", false, "print JSON")
				},
				Func: runRepoMirrorList,
			},
			{
				Name: "rm", Desc: "Remove one tracker mirror from the server (admin; the repository's own tracker is kept)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					c.StrOpt(&repoMirrorOpts.server, "server", "", "", "server URL override")
					c.BoolOpt(&repoMirrorOpts.yes, "yes", "y", false, "confirm the removal")
					c.AddArg("tracker_id", "tracker_id of the mirror (see `gofer repo mirror ls`)", true)
				},
				Func: runRepoMirrorRemove,
			},
		},
	}
}

func runRepoMirrorList(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, syncServerOrEnv(repoMirrorOpts.server), os.Getenv("GOFER_SERVER_TOKEN"))
	if err != nil {
		return err
	}
	repos, err := cli.ListTrackerRepos()
	if err != nil {
		return err
	}
	if repoMirrorOpts.asJSON {
		return printTrackerJSON(c, repos)
	}
	if len(repos) == 0 {
		c.Println("no tracker mirrors on the server")
		return nil
	}
	for _, r := range repos {
		synced := "never"
		if r.LastSyncAt > 0 {
			synced = time.Unix(r.LastSyncAt, 0).Format("2006-01-02 15:04")
		}
		c.Printf("%-24s prefix=%-8s project=%-16s path=%s synced=%s\n", r.TrackerID, r.Prefix, r.ProjectKey, r.RelPath, synced)
	}
	return nil
}

func runRepoMirrorRemove(c *gcli.Command, _ []string) error {
	id := c.Arg("tracker_id").String()
	if !repoMirrorOpts.yes {
		return fmt.Errorf("repo mirror rm deletes every issue and memory of mirror %s on the server; re-run with --yes", id)
	}
	cli, err := newClient(config.InputCfgFile, syncServerOrEnv(repoMirrorOpts.server), os.Getenv("GOFER_SERVER_TOKEN"))
	if err != nil {
		return err
	}
	res, err := cli.DeleteTrackerRepo(id)
	if err != nil {
		return err
	}
	c.Printf("mirror %s removed: %d issues, %d memories, %d memory suggestions\n", res.TrackerID,
		res.Deleted.Issues, res.Deleted.Memories, res.Deleted.Suggestions)
	return nil
}
