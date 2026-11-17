package commands

import (
	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/job"
)

// printSelfResult renders the outcome the job's agent reported about itself
// (gofer_submit_result) in `job show`: the decision with its reason, then what a person
// has to do and the findings when it gave any. Nothing is printed without a report.
func printSelfResult(c *gcli.Command, res job.JobResult) {
	if res.ResultDecision == "" {
		return
	}
	c.Printf("reported:   %s — %s\n", res.ResultDecision, res.ResultReason)
	var in job.SelfResultInput
	if !job.DecodeSelfResultReport(res.ResultReportJSON, &in) {
		return
	}
	if in.ActionRequired != "" {
		c.Printf("            action required: %s\n", in.ActionRequired)
	}
	for _, f := range in.Findings {
		c.Printf("            - %s\n", f)
	}
}
