package workbench

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

func openWorkbenchStore(t *testing.T) *jobstore.Store {
	t.Helper()
	store, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func putWorkbenchJob(t *testing.T, store *jobstore.Store, rec jobstore.JobRecord, title, prompt string) {
	t.Helper()
	req := job.JobRequest{
		ProjectKey: rec.ProjectKey,
		Agent:      rec.Agent,
		Runner:     rec.Runner,
		Prompt:     prompt,
		Title:      title,
		Cwd:        rec.Cwd,
	}
	raw, err := json.Marshal(req)
	assert.NoErr(t, err)
	rec.RequestJSON = string(raw)
	if rec.ResultDir == "" {
		rec.ResultDir = filepath.Join(t.TempDir(), rec.ID)
	}
	assert.NoErr(t, store.UpsertJob(rec))
}

func TestListProjectsThreadsAndAttention(t *testing.T) {
	store := openWorkbenchStore(t)
	now := time.Now().Unix()
	putWorkbenchJob(t, store, jobstore.JobRecord{
		ID: "first", ProjectKey: "alpha", Agent: "cli", Runner: "local", Cwd: ".",
		SessionID: "sess-1", Interactive: true, Status: job.StatusDone, StartedAt: now - 300, EndedAt: now - 290, UpdatedAt: now - 290,
	}, "123456789012345678901234567890EXTRA", "first")
	putWorkbenchJob(t, store, jobstore.JobRecord{
		ID: "latest", ProjectKey: "alpha", Agent: "cli", Runner: "local", Cwd: ".",
		SessionID: "sess-1", Interactive: true, ResumedFrom: "first", Status: job.StatusRunning, StartedAt: now - 200, UpdatedAt: now - 190,
		UsageJSON: `{"input_tokens":4,"output_tokens":6,"total_tokens":10,"cost_usd":0.25}`,
	}, "later", "second")
	assert.NoErr(t, store.UpsertInteraction(jobstore.InteractionRecord{
		ID: "int-1", JobID: "latest", Type: job.InteractionTypeQuestion, Prompt: "answer?",
		Status: job.InteractionPending, CreatedAt: now - 180,
	}))
	assert.NoErr(t, store.UpsertWorkbenchThreadPref(jobstore.WorkbenchThreadPref{
		CallerID: "alice", ThreadID: "s:sess-1", Pinned: true,
	}))
	plan := jobstore.Plan{
		PlanID: "plan-alpha-001", Title: "alpha", ProjectKey: "alpha", Status: jobstore.PlanOpen,
		CreatedAt: now - 100, UpdatedAt: now - 100,
	}
	assert.NoErr(t, store.InsertPlan(plan))
	decision := jobstore.PlanDecision{
		ID: "decision-alpha", PlanID: plan.PlanID, Title: "choose", Question: "which?",
		State: jobstore.DecisionOpen, AskedAt: now - 90, TimeoutSec: 3600,
	}
	assert.NoErr(t, store.InsertDecision(&decision))

	service := NewService(store, nil, nil)
	got, err := service.List("alice", Query{Since: 1})
	assert.NoErr(t, err)
	assert.Len(t, got.Projects, 1)
	alpha := got.Projects[0]
	assert.Eq(t, "alpha", alpha.ProjectKey)
	assert.Eq(t, StatusBlocked, alpha.Status)
	assert.Eq(t, 1, alpha.Counts.Blocked)
	assert.Eq(t, 1, alpha.Counts.OrphanBlocked)
	assert.Len(t, alpha.Threads, 1)
	thread := alpha.Threads[0]
	assert.Eq(t, "s:sess-1", thread.ID)
	assert.Eq(t, KindAgent, thread.Kind)
	assert.Eq(t, StatusBlocked, thread.Status)
	assert.Eq(t, "123456789012345678901234567890", thread.Title)
	assert.Eq(t, 2, thread.Turns)
	assert.True(t, thread.Pinned)
	assert.NotNil(t, thread.Usage)
	assert.Eq(t, int64(10), thread.Usage.TotalTokens)
	assert.Len(t, thread.PendingInteractions, 1)
	assert.Len(t, got.Attention, 1)
	assert.Eq(t, ActionAnswer, got.Attention[0].Action)
	assert.Eq(t, "int-1", got.Attention[0].InteractionID)
}

func TestPatchThreadRenameAndSeen(t *testing.T) {
	store := openWorkbenchStore(t)
	now := time.Now().Unix()
	service := NewService(store, nil, nil)
	service.now = func() time.Time { return time.Unix(now, 0) }
	_, err := service.List("alice", Query{Since: 1})
	assert.NoErr(t, err)
	putWorkbenchJob(t, store, jobstore.JobRecord{
		ID: "done", ProjectKey: "alpha", Agent: "cli", Runner: "local", Cwd: ".",
		SessionID: "sess-done", Interactive: true, Status: job.StatusDone, StartedAt: now - 20, EndedAt: now + 10, UpdatedAt: now + 10,
		CommitsJSON: `[{"sha":"done","subject":"change"}]`,
	}, "default", "done")
	before, err := service.List("alice", Query{Since: 1})
	assert.NoErr(t, err)
	assert.Eq(t, StatusReview, before.Projects[0].Threads[0].Status)
	service.now = func() time.Time { return time.Unix(now+20, 0) }
	title, seen, pinned := "renamed", true, true
	_, err = service.Patch("alice", "s:sess-done", PatchInput{Title: &title, Seen: &seen, Pinned: &pinned})
	assert.NoErr(t, err)
	after, err := service.List("alice", Query{Since: 1})
	assert.NoErr(t, err)
	thread := after.Projects[0].Threads[0]
	assert.Eq(t, "renamed", thread.Title)
	assert.Eq(t, StatusDone, thread.Status)
	assert.True(t, thread.Pinned)
	assert.Eq(t, now+20, thread.SeenAt)
}

type recordingJobs struct {
	jobID    string
	prompt   string
	callerID string
}

func (r *recordingJobs) ResumeJob(jobID, prompt, _ string, callerID string) (job.JobResult, error) {
	r.jobID, r.prompt, r.callerID = jobID, prompt, callerID
	return job.JobResult{ID: "continued-job"}, nil
}

func (r *recordingJobs) SaySession(_, _ string) error { return nil }

type recordingRelay struct {
	sessionID string
	answer    string
	callerID  string
}

func (r *recordingRelay) Say(sessionID, answer, callerID string) (jobstore.PlanDecision, error) {
	r.sessionID, r.answer, r.callerID = sessionID, answer, callerID
	return jobstore.PlanDecision{ID: "answered-decision"}, nil
}

func TestTurnDispatchesToExistingOwners(t *testing.T) {
	store := openWorkbenchStore(t)
	putWorkbenchJob(t, store, jobstore.JobRecord{
		ID: "source", ProjectKey: "alpha", Agent: "cli", Runner: "local", Cwd: ".",
		SessionID: "sess-turn", Interactive: true, Status: job.StatusDone, StartedAt: 100, EndedAt: 110, UpdatedAt: 110,
	}, "source", "first")
	jobs := &recordingJobs{}
	relay := &recordingRelay{}
	service := NewService(store, jobs, relay)

	jobTurn, err := service.Turn("alice", "s:sess-turn", "continue")
	assert.NoErr(t, err)
	assert.Eq(t, "continued-job", jobTurn.JobID)
	assert.Eq(t, "source", jobs.jobID)
	assert.Eq(t, "continue", jobs.prompt)
	assert.Eq(t, "alice", jobs.callerID)

	relayTurn, err := service.Turn("alice", "r:relay-turn", "answer")
	assert.NoErr(t, err)
	assert.Eq(t, "answered-decision", relayTurn.DecisionID)
	assert.Eq(t, "relay-turn", relay.sessionID)

	_, err = service.Turn("alice", "j:source", "cannot")
	assert.True(t, errors.Is(err, ErrNotResumable))
}
