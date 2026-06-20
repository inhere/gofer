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
	if code := app.Run(NormalizeArgs(app, args)); code != 0 {
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
