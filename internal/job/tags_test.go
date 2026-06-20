package job

import (
	"reflect"
	"testing"
	"time"
)

// TestSubmitTagsRoundTrip proves Tags submitted on a JobRequest survive into the
// JobResult (live in-memory) and through the DB read path (after eviction).
func TestSubmitTagsRoundTrip(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Tags: []string{"a", "b"}, Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done, got %s", final.Status)
	}
	// Job is terminal -> evicted; Get goes through fromRecord (DB read path).
	if e := s.entry(final.ID); e != nil {
		t.Fatalf("setup: expected job evicted after terminal")
	}
	got, ok := s.Get(final.ID)
	if !ok {
		t.Fatalf("Get after eviction: job not found")
	}
	if !reflect.DeepEqual(got.Tags, []string{"a", "b"}) {
		t.Fatalf("tags did not round-trip through DB: got %#v want [a b]", got.Tags)
	}
}

// TestSubmitNoTagsIsNil proves a job with no tags reads back with Tags == nil
// (so the API omitempty drops the field).
func TestSubmitNoTagsIsNil(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: expected done, got %s", final.Status)
	}
	got, ok := s.Get(final.ID)
	if !ok {
		t.Fatalf("Get: job not found")
	}
	if got.Tags != nil {
		t.Fatalf("expected nil Tags for a job submitted without tags, got %#v", got.Tags)
	}
}

// TestListJobsFiltersLiveAndPersisted is the key two-path test (P1-b): a running
// (non-terminal, in-memory live) job and an already-persisted (terminal) job are
// both filtered correctly by the new tag/agent/runner/since dimensions. The live
// job exercises the in-memory overlay filter; the persisted one exercises the DB
// WHERE — both paths must agree.
func TestListJobsFiltersLiveAndPersisted(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	// Persisted/terminal job: tags [done-tag], agent exec, runner local.
	persisted := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Tags: []string{"done-tag"}, Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if persisted.Status != StatusDone {
		t.Fatalf("setup: persisted job should be done, got %s", persisted.Status)
	}

	// Live/non-terminal job: tags [live-tag], a long sleep so it stays running
	// in-memory for the duration of the assertions.
	live, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Tags: []string{"live-tag"}, Cmd: []string{"sleep", "5"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("Submit live: %v", err)
	}
	defer func() { _ = s.Cancel(live.ID); s.Wait(live.ID) }()
	waitForStatus(t, s, live.ID, StatusRunning, 2*time.Second)

	// tag=done-tag -> only the persisted job (DB path matches, overlay excludes live).
	doneByTag, err := s.ListJobs(ListOpts{Tag: "done-tag"})
	if err != nil {
		t.Fatalf("ListJobs(tag=done-tag): %v", err)
	}
	if len(doneByTag) != 1 || doneByTag[0].ID != persisted.ID {
		t.Fatalf("tag=done-tag should yield only persisted job, got %+v", doneByTag)
	}

	// tag=live-tag -> only the live job (overlay path matches; DB has no such row).
	liveByTag, err := s.ListJobs(ListOpts{Tag: "live-tag"})
	if err != nil {
		t.Fatalf("ListJobs(tag=live-tag): %v", err)
	}
	if len(liveByTag) != 1 || liveByTag[0].ID != live.ID {
		t.Fatalf("tag=live-tag should yield only live job, got %+v", liveByTag)
	}
	if liveByTag[0].Status != StatusRunning {
		t.Fatalf("live job should be running, got %s", liveByTag[0].Status)
	}

	// runner=worker -> none (both jobs run on local); proves the overlay applies
	// the runner filter to the live job too.
	workerOnly, err := s.ListJobs(ListOpts{Runner: "worker"})
	if err != nil {
		t.Fatalf("ListJobs(runner=worker): %v", err)
	}
	if len(workerOnly) != 0 {
		t.Fatalf("runner=worker should be empty, got %+v", workerOnly)
	}

	// agent=exec -> both jobs (DB persisted + live overlay).
	execBoth, err := s.ListJobs(ListOpts{Agent: "exec"})
	if err != nil {
		t.Fatalf("ListJobs(agent=exec): %v", err)
	}
	if len(execBoth) != 2 {
		t.Fatalf("agent=exec should yield both jobs, got %d: %+v", len(execBoth), execBoth)
	}

	// since far in the future -> none, on both paths.
	future := time.Now().Add(time.Hour).Unix()
	sinceNone, err := s.ListJobs(ListOpts{Since: future})
	if err != nil {
		t.Fatalf("ListJobs(since=future): %v", err)
	}
	if len(sinceNone) != 0 {
		t.Fatalf("since=future should be empty, got %+v", sinceNone)
	}
}
