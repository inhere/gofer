package client

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
)

// TestListJobsParsesAndFilters submits two tagged jobs and asserts ListJobs
// unwraps the {"jobs":[...]} envelope and that the tag filter is threaded into
// the query string (only the matching job comes back).
func TestListJobsParsesAndFilters(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	a, err := c.SubmitJob(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		Tags: []string{"alpha"},
	})
	if err != nil {
		t.Fatalf("SubmitJob a: %v", err)
	}
	b, err := c.SubmitJob(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		Tags: []string{"beta"},
	})
	if err != nil {
		t.Fatalf("SubmitJob b: %v", err)
	}
	waitDone(t, c, a.ID)
	waitDone(t, c, b.ID)

	// No filter -> both jobs.
	all, err := c.ListJobs(job.ListOpts{})
	if err != nil {
		t.Fatalf("ListJobs all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListJobs all expected 2, got %d: %+v", len(all), all)
	}

	// Tag filter reaches the server (query string), returning only the match.
	byTag, err := c.ListJobs(job.ListOpts{Tag: "alpha"})
	if err != nil {
		t.Fatalf("ListJobs tag: %v", err)
	}
	if len(byTag) != 1 || byTag[0].ID != a.ID {
		t.Fatalf("tag=alpha filter wrong: %+v", byTag)
	}
	if !reflect.DeepEqual(byTag[0].Tags, []string{"alpha"}) {
		t.Fatalf("tags not echoed: %+v", byTag[0].Tags)
	}

	// A filter the server applies but nothing matches -> empty, proving the param
	// is actually sent (not dropped).
	none, err := c.ListJobs(job.ListOpts{Agent: "claude"})
	if err != nil {
		t.Fatalf("ListJobs agent: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("agent=claude expected 0, got %d", len(none))
	}
}

// TestGetJobRequestRoundTrip submits a job then reads back its original request
// via the P2-b endpoint.
func TestGetJobRequestRoundTrip(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	created, err := c.SubmitJob(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		Tags: []string{"x", "y"},
	})
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}
	waitDone(t, c, created.ID)

	req, err := c.GetJobRequest(created.ID)
	if err != nil {
		t.Fatalf("GetJobRequest: %v", err)
	}
	if req.ProjectKey != "self" || req.Agent != "exec" {
		t.Fatalf("request fields wrong: %+v", req)
	}
	if !reflect.DeepEqual(req.Cmd, []string{"go", "version"}) {
		t.Fatalf("cmd not round-tripped: %+v", req.Cmd)
	}
	if !reflect.DeepEqual(req.Tags, []string{"x", "y"}) {
		t.Fatalf("tags not round-tripped: %+v", req.Tags)
	}

	// Unknown id -> error (404 surfaced).
	if _, err := c.GetJobRequest("nope"); err == nil {
		t.Fatal("expected error for unknown job request")
	}
}

// TestStreamJobOrderedAndStops streams a real job to terminal: the callback sees
// at least one status frame and the final end frame, in order, and StreamJob
// returns once end arrives.
func TestStreamJobOrderedAndStops(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	created, err := c.SubmitJob(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var events []string
	err = c.StreamJob(ctx, created.ID, 0, func(ev SSEEvent) {
		events = append(events, ev.Event)
	})
	if err != nil {
		t.Fatalf("StreamJob: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no SSE events received")
	}
	// First frame is a status snapshot; last frame is the terminal end.
	if events[0] != "status" {
		t.Fatalf("first event=%q, want status", events[0])
	}
	if events[len(events)-1] != "end" {
		t.Fatalf("last event=%q, want end", events[len(events)-1])
	}
}

// TestStreamJobCtxCancelStops cancels the context mid-stream (a never-terminating
// job) and asserts StreamJob returns promptly with no error.
func TestStreamJobCtxCancelStops(t *testing.T) {
	ts := newServer(t, testToken, false)
	c := New(ts.URL, testToken)

	created, err := c.SubmitJob(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "30"}, Cwd: ".", TimeoutSec: 60,
	})
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}
	t.Cleanup(func() {
		_, _ = c.CancelJob(created.ID)
		waitDone(t, c, created.ID)
	})

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel shortly after the first status frame arrives.
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		done <- c.StreamJob(ctx, created.ID, 0, func(SSEEvent) {})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StreamJob after ctx cancel returned err=%v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("StreamJob did not return after ctx cancel")
	}
}
