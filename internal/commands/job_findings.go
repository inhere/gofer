package commands

import (
	"fmt"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/tracker"
)

// jobFindingsOpts holds `job findings` flags (gofer-3nxa.3).
var jobFindingsOpts = struct {
	create   bool
	priority int
	tag      string
}{priority: 2, tag: "discovered"}

// runJobFindings lists the items of the 「发现但不碰」 section of a job's report (the
// stdout tail `job review` reads). With --create-issues each item becomes an issue in the
// tracker of the CURRENT repository (the one the caller stands in — the server has no
// tracker write endpoint), its description naming the job, and the new ids are printed.
func runJobFindings(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("job findings requires an <id> argument")
	}
	if jobFindingsOpts.priority < 0 || jobFindingsOpts.priority > 4 {
		return fmt.Errorf("--priority must be 0..4")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	report, err := cli.GetLogsTail(id, "stdout", reviewLogBytes)
	if err != nil {
		return fmt.Errorf("read the report of job %s: %w", id, err)
	}
	findings := job.ParseFindings(report)
	if len(findings) == 0 {
		c.Printf("job %s reported no 「%s」 items\n", id, job.FindingsSectionTitle)
		return nil
	}
	if !jobFindingsOpts.create {
		for i, f := range findings {
			c.Printf("%d. %s\n", i+1, f)
		}
		return nil
	}

	store, err := tracker.Discover(".", "")
	if err != nil {
		return fmt.Errorf("--create-issues: %w", err)
	}
	for _, f := range findings {
		item, err := store.CreateIssue(tracker.Issue{
			Title:       findingTitle(f),
			Type:        "task",
			Priority:    jobFindingsOpts.priority,
			Description: findingDescription(f, id),
			Tags:        tracker.ParseTags(jobFindingsOpts.tag),
			CreatedBy:   trackerActor(),
		})
		if err != nil {
			return fmt.Errorf("create issue for %q: %w", f, err)
		}
		c.Printf("%s %s\n", item.ID, item.Title)
	}
	tryAutoSync(c, store)
	return nil
}

// findingTitleMaxRunes keeps an issue title readable; the full finding text always
// goes into the description.
const findingTitleMaxRunes = 120

func findingTitle(f string) string {
	r := []rune(strings.Join(strings.Fields(f), " "))
	if len(r) > findingTitleMaxRunes {
		return string(r[:findingTitleMaxRunes-1]) + "…"
	}
	return string(r)
}

func findingDescription(f, jobID string) string {
	return "discovered in job " + jobID + "\n\n" + f
}
