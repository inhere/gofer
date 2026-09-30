package workbench

import (
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

type sessionRecordingJobs struct {
	resumeCount int
	sayID       string
	sayText     string
}

func (r *sessionRecordingJobs) ResumeJob(_, _, _, _ string) (job.JobResult, error) {
	r.resumeCount++
	return job.JobResult{ID: "wrong-new-job"}, nil
}

func (r *sessionRecordingJobs) SaySession(id, text string) error {
	r.sayID, r.sayText = id, text
	return nil
}

func TestWorkbenchContinuousACPReusesSessionJob(t *testing.T) {
	store := openWorkbenchStore(t)
	now := time.Now().Unix()
	putWorkbenchJob(t, store, jobstore.JobRecord{
		ID: "resident", ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		SessionID: "sid-1", SessionStateJSON: `{"session":true,"turn_no":2}`,
		Status: job.StatusAwaitingInput, StartedAt: now - 10, UpdatedAt: now,
	}, "resident", "first")
	jobs := &sessionRecordingJobs{}
	service := NewService(store, jobs, nil)
	result, err := service.Turn("alice", "s:sid-1", "third")
	if err != nil || result.JobID != "resident" || jobs.sayID != "resident" || jobs.sayText != "third" || jobs.resumeCount != 0 {
		t.Fatalf("continuous turn: result=%+v err=%v jobs=%+v", result, err, jobs)
	}
	listed, err := service.List("alice", Query{Since: 1})
	if err != nil {
		t.Fatal(err)
	}
	thread := listed.Projects[0].Threads[0]
	if thread.LatestJobID != "resident" || thread.Turns != 2 || thread.Status != StatusBlocked {
		t.Fatalf("continuous thread projection=%+v", thread)
	}
}

func TestWorkbenchContinuousACPShowsProcessLink(t *testing.T) {
	store := openWorkbenchStore(t)
	putWorkbenchJob(t, store, jobstore.JobRecord{
		ID: "process-id", ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		SessionID: "sid-process", SessionStateJSON: `{"session":true,"turn_no":1}`,
		Status: job.StatusAwaitingInput, StartedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}, "resident", "first")
	service := NewService(store, &sessionRecordingJobs{}, nil)
	listed, err := service.List("alice", Query{Since: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := listed.Projects[0].Threads[0].LatestJobID; got != "process-id" {
		t.Fatalf("process link target=%q", got)
	}
}
