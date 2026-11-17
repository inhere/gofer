package commands

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// job diagnose (R10): why did this job end, what else was in the chain, when did each
// stage happen, and what should I do next — all derived server-side, printed here.
//
// The printing itself lives in printJobDiagnosis so a test can assert the screen holds
// the termination reason and the next steps without a server.

var jobDiagnoseOpts struct {
	jsonOut bool
}

func runJobDiagnose(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("job diagnose requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	d, err := cli.GetJobDiagnosis(id)
	if err != nil {
		return err
	}
	if jobDiagnoseOpts.jsonOut {
		b, err := json.MarshalIndent(d, "", "  ")
		if err != nil {
			return err
		}
		c.Println(string(b))
		return nil
	}
	printJobDiagnosis(c, d)
	return nil
}

// printJobDiagnosis prints one diagnosis: the job, the termination reason (enum plus its
// label), the timeline, the chain with the earliest failure, then the evidence, what is
// unknown and the next steps. A stage / section gofer had no record of prints its own
// "unknown" note instead of an empty line.
func printJobDiagnosis(c *gcli.Command, d job.Diagnosis) {
	c.Printf("id:          %s\n", d.JobID)
	c.Printf("status:      %s\n", d.Status)
	c.Printf("reason:      %s (%s)\n", d.Reason, d.ReasonLabel)
	if d.Error != "" {
		c.Printf("error:       %s\n", d.Error)
	}
	c.Println(cliText("cli.job.diagnose.timeline"))
	for _, st := range d.Timeline {
		if st.Present {
			c.Printf("  %-13s %s\n", st.Label+":", formatDiagnoseTime(st.At))
			continue
		}
		c.Printf("  %-13s %s\n", st.Label+":", cliText("cli.job.diagnose.unknown_stage", "note", st.Note))
	}
	c.Println(cliText("cli.job.diagnose.chain"))
	for _, l := range d.Chain {
		line := fmt.Sprintf("  %s  %s  %s", l.JobID, l.Status, l.Reason)
		if l.Relation != "" {
			line += "  [" + l.Relation + "]"
		}
		if l.Error != "" {
			line += "  " + l.Error
		}
		c.Println(line)
	}
	if d.FirstFailure != nil {
		f := d.FirstFailure
		c.Println(cliText("cli.job.diagnose.first_failure", "id", f.JobID, "reason", f.Reason, "error", f.Error))
	}
	if len(d.Evidence) > 0 {
		c.Println(cliText("cli.job.diagnose.evidence"))
		for _, ev := range d.Evidence {
			c.Println("  - " + ev)
		}
	}
	if len(d.Unknowns) > 0 {
		c.Println(cliText("cli.job.diagnose.unknown"))
		for _, u := range d.Unknowns {
			c.Println("  - " + u)
		}
	}
	if len(d.NextSteps) > 0 {
		c.Println(cliText("cli.job.diagnose.next_steps"))
		for _, s := range d.NextSteps {
			c.Println("  - " + s)
		}
	}
}

// formatDiagnoseTime renders a unix-SECONDS stamp the way the rest of the CLI prints job
// times, in local time; 0 (no record) is never formatted as 1970.
func formatDiagnoseTime(at int64) string {
	if at <= 0 {
		return cliText("cli.job.diagnose.stage_unknown")
	}
	return time.Unix(at, 0).Format("2006-01-02 15:04:05")
}
