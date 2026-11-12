package commands

import (
	"encoding/json"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/job"
)

// printJobFallbackSkipped lists the failover candidates the job dropped without trying
// (job.fallback_skipped events): the reason a job that "should have switched agent" just
// failed. Best-effort: an events read that fails prints nothing — `job show` never fails
// because of an extra line.
func printJobFallbackSkipped(c *gcli.Command, cli *client.Client, jobID string) {
	events, err := cli.ListJobEvents(jobID)
	if err != nil {
		return
	}
	for _, ev := range events {
		if ev.Type != job.EventJobFallbackSkipped {
			continue
		}
		var d struct {
			Agent  string `json:"agent"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal([]byte(ev.Detail), &d) != nil {
			continue
		}
		c.Printf("fallback_skipped: %s (%s)\n", d.Agent, d.Reason)
	}
}
