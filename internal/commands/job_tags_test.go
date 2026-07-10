package commands

import (
	"reflect"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/job"
)

// TestJobRunTagsMapping asserts `job run --tags a,b` parses into the constructed
// JobRequest.Tags == ["a","b"] (E5 P2-d, the tag-setting entry). It runs the
// real gcli arg pipeline so flag binding + the splitLabels mapping are both
// exercised, without hitting the network.
func TestJobRunTagsMapping(t *testing.T) {
	// Reset shared flag state so the test does not leak / inherit.
	jobRunOpts.project, jobRunOpts.agent, jobRunOpts.runner = "", "", ""
	jobRunOpts.cwd, jobRunOpts.prompt, jobRunOpts.tags = "", "", ""

	app := NewApp("test")
	var gotTags []string
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		// Mirror submitJSONJob's tag mapping: comma value -> splitLabels -> Tags.
		req := job.JobRequest{Tags: splitLabels(jobRunOpts.tags)}
		gotTags = req.Tags
		return nil
	}

	args := []string{"job", "run", "-p", "self", "-a", "exec", "--tags", "a,b", "--", "go", "version"}
	if code := app.Run(args); code != 0 {
		t.Fatalf("app.Run exit code=%d for args %v", code, args)
	}
	if !reflect.DeepEqual(gotTags, []string{"a", "b"}) {
		t.Fatalf("JobRequest.Tags=%v want [a b]", gotTags)
	}
}

// TestJobRunNoTagsOmitsField: without --tags the constructed Tags is nil (the
// JobRequest omitempty drops the field), so an untagged submit stays clean.
func TestJobRunNoTagsOmitsField(t *testing.T) {
	jobRunOpts.tags = ""
	if got := splitLabels(jobRunOpts.tags); got != nil {
		t.Fatalf("empty --tags should yield nil, got %v", got)
	}
}

// TestJobListSessionMapping asserts `job list --session <id>` binds jobListOpts.session
// and maps into job.ListOpts.Session (P3). It runs the real gcli arg pipeline so the
// flag binding is exercised, with a capturing Func that never hits the network.
func TestJobListSessionMapping(t *testing.T) {
	// Reset shared flag state so the test does not leak / inherit.
	jobListOpts.session = ""

	app := NewApp("test")
	var gotOpts job.ListOpts
	listCmd := app.GetCommand("job").GetCommand("list")
	listCmd.Func = func(c *gcli.Command, _ []string) error {
		// Mirror runJobList's mapping (the session dimension under test).
		gotOpts = job.ListOpts{Session: jobListOpts.session}
		return nil
	}

	args := []string{"job", "list", "--session", "sess-cli-xyz"}
	if code := app.Run(args); code != 0 {
		t.Fatalf("app.Run exit code=%d for args %v", code, args)
	}
	if jobListOpts.session != "sess-cli-xyz" {
		t.Fatalf("--session did not bind: jobListOpts.session=%q", jobListOpts.session)
	}
	if gotOpts.Session != "sess-cli-xyz" {
		t.Fatalf("ListOpts.Session=%q want sess-cli-xyz", gotOpts.Session)
	}
}

func TestJobListSourceJobMapping(t *testing.T) {
	// Reset shared flag state so the test does not leak / inherit.
	jobListOpts.sourceJob = ""

	app := NewApp("test")
	var gotOpts job.ListOpts
	listCmd := app.GetCommand("job").GetCommand("list")
	listCmd.Func = func(c *gcli.Command, _ []string) error {
		// Mirror runJobList's mapping (the source job dimension under test).
		gotOpts = job.ListOpts{SourceJob: jobListOpts.sourceJob}
		return nil
	}

	args := []string{"job", "list", "--source-job", "job-src"}
	if code := app.Run(args); code != 0 {
		t.Fatalf("app.Run exit code=%d for args %v", code, args)
	}
	if jobListOpts.sourceJob != "job-src" {
		t.Fatalf("--source-job did not bind: jobListOpts.sourceJob=%q", jobListOpts.sourceJob)
	}
	if gotOpts.SourceJob != "job-src" {
		t.Fatalf("ListOpts.SourceJob=%q want job-src", gotOpts.SourceJob)
	}
}
