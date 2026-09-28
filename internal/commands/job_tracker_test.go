package commands

import "testing"

func TestResolveJobTrackerIDMissingSuggestsFlag(t *testing.T) {
	t.Chdir(t.TempDir())
	jobRunOpts.cwd = "."
	jobRunOpts.trackerID = ""
	got, err := resolveJobTrackerID()
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("tracker id = %q, want empty", got)
	}
}
