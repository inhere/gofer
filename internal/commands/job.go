package commands

import "github.com/gookit/gcli/v3"

// NewJobCmd builds the `job` command group (run/show/logs/cancel). P6 logic.
func NewJobCmd() *gcli.Command {
	return &gcli.Command{
		Name: "job",
		Desc: "Submit and manage jobs via the bridge server",
		Subs: []*gcli.Command{
			{
				Name: "run",
				Desc: "Submit a new job",
				Func: notImplemented("job run", "P6"),
			},
			{
				Name: "show",
				Desc: "Query a job's status",
				Func: notImplemented("job show", "P6"),
			},
			{
				Name: "logs",
				Desc: "Read a job's stdout/stderr logs",
				Func: notImplemented("job logs", "P6"),
			},
			{
				Name: "cancel",
				Desc: "Cancel a running job",
				Func: notImplemented("job cancel", "P6"),
			},
		},
	}
}
