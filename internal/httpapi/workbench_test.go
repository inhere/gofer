package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	acprunner "github.com/inhere/gofer/internal/runner/acp"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

type workbenchTestCounts struct {
	Total         int `json:"total"`
	Blocked       int `json:"blocked"`
	Working       int `json:"working"`
	Review        int `json:"review"`
	Done          int `json:"done"`
	Idle          int `json:"idle"`
	OrphanBlocked int `json:"orphan_blocked"`
}

type workbenchTestThread struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Status      string   `json:"status"`
	Title       string   `json:"title"`
	ProjectKey  string   `json:"project_key"`
	Agent       string   `json:"agent"`
	Turns       int      `json:"turns"`
	Stalled     bool     `json:"stalled"`
	Pinned      bool     `json:"pinned"`
	SeenAt      int64    `json:"seen_at"`
	LatestJobID string   `json:"latest_job_id"`
	JobIDs      []string `json:"job_ids"`
}

type workbenchTestProject struct {
	ProjectKey string                `json:"project_key"`
	Status     string                `json:"status"`
	Counts     workbenchTestCounts   `json:"counts"`
	Threads    []workbenchTestThread `json:"threads"`
}

type workbenchTestAttention struct {
	ThreadID     string `json:"thread_id"`
	Action       string `json:"action"`
	WaitingSince int64  `json:"waiting_since"`
	JobID        string `json:"job_id"`
	Interaction  string `json:"interaction_id"`
	SessionID    string `json:"session_id"`
}

type workbenchTestResponse struct {
	Projects  []workbenchTestProject   `json:"projects"`
	Attention []workbenchTestAttention `json:"attention"`
	Total     int                      `json:"total"`
	Since     int64                    `json:"since"`
}

type workbenchTestTurnResult struct {
	ThreadID   string `json:"thread_id"`
	JobID      string `json:"job_id"`
	DecisionID string `json:"decision_id"`
}

func newWorkbenchTestServer(t *testing.T, sc config.ServerConfig) *Server {
	t.Helper()
	root := t.TempDir()
	helper := testcmd.Path(t)
	cfg := &config.Config{
		Server:  sc,
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec", "cli", "acp"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"cli": {
				Type:          agent.TypeCLIAgent,
				Command:       helper,
				Args:          []string{"printf", "{{prompt}}"},
				SessionResume: []string{"printf", "{{prompt}}"},
			},
			"acp": {
				Type:    agent.TypeACPAgent,
				Command: helper,
				Args:    []string{"acp-fake"},
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{
		localrunner.Name: localrunner.New(),
		acprunner.Name:   acprunner.New(),
	}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	return New(&cfg.Server, sc.Token, sc.AllowEmptyToken, jobs, eng, projects, agents, nil, nil, nil, nil)
}

func seedWorkbenchJob(t *testing.T, s *Server, rec jobstore.JobRecord, title, prompt string) {
	t.Helper()
	if rec.ProjectKey == "" {
		rec.ProjectKey = "self"
	}
	if rec.Agent == "" {
		rec.Agent = "cli"
	}
	if rec.Runner == "" {
		rec.Runner = "local"
	}
	if rec.Cwd == "" {
		rec.Cwd = "."
	}
	if rec.ResultDir == "" {
		rec.ResultDir = filepath.Join(t.TempDir(), rec.ID)
	}
	if rec.UpdatedAt == 0 {
		rec.UpdatedAt = rec.StartedAt
	}
	if rec.CallerID == "" {
		rec.CallerID = "default"
	}
	req := job.JobRequest{
		ProjectKey: rec.ProjectKey,
		Agent:      rec.Agent,
		Runner:     rec.Runner,
		Prompt:     prompt,
		Cwd:        rec.Cwd,
		Title:      title,
		TimeoutSec: 30,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rec.RequestJSON = string(raw)
	if job.IsTerminal(rec.Status) && rec.EndedAt == 0 {
		rec.EndedAt = rec.UpdatedAt
	}
	if err := s.jobs.Meta().UpsertJob(rec); err != nil {
		t.Fatalf("seed job %s: %v", rec.ID, err)
	}
	if !job.IsTerminal(rec.Status) {
		t.Cleanup(func() {
			rec.Status = job.StatusDone
			rec.EndedAt = rec.UpdatedAt + 1
			rec.UpdatedAt++
			_ = s.jobs.Meta().UpsertJob(rec)
		})
	}
}

func seedWorkbenchRelay(t *testing.T, s *Server, sessionID, projectKey, state string, startedAt, seenAt int64) {
	t.Helper()
	_, err := s.jobs.Meta().UpsertAgentSession(jobstore.AgentSession{
		SessionID:  sessionID,
		Agent:      "claude",
		ProjectKey: projectKey,
		Runner:     "local",
		Cwd:        s.projects.Config().Projects["self"].HostPath,
		CallerID:   "default",
		State:      state,
		RelayMode:  "on",
		StartedAt:  startedAt,
		LastSeenAt: seenAt,
	})
	if err != nil {
		t.Fatalf("seed relay %s: %v", sessionID, err)
	}
}

func getWorkbenchThreads(t *testing.T, s *Server, token, query string) workbenchTestResponse {
	t.Helper()
	path := "/v1/workbench/threads"
	if query != "" {
		path += "?" + query
	}
	resp := do(t, s, http.MethodGet, path, token, nil)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("GET %s status=%d, want 200: %s", path, resp.StatusCode, body)
	}
	var out workbenchTestResponse
	decode(t, resp, &out)
	return out
}

func findWorkbenchProject(t *testing.T, got workbenchTestResponse, key string) workbenchTestProject {
	t.Helper()
	for _, p := range got.Projects {
		if p.ProjectKey == key {
			return p
		}
	}
	t.Fatalf("project %q missing: %+v", key, got.Projects)
	return workbenchTestProject{}
}

func findWorkbenchThread(t *testing.T, got workbenchTestResponse, id string) workbenchTestThread {
	t.Helper()
	for _, p := range got.Projects {
		for _, thread := range p.Threads {
			if thread.ID == id {
				return thread
			}
		}
	}
	t.Fatalf("thread %q missing: %+v", id, got.Projects)
	return workbenchTestThread{}
}

func patchWorkbenchThread(t *testing.T, s *Server, token, id string, body any) {
	t.Helper()
	resp := do(t, s, http.MethodPatch, "/v1/workbench/threads/"+url.PathEscape(id), token, body)
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("PATCH thread %s status=%d, want 200: %s", id, resp.StatusCode, data)
	}
	resp.Body.Close()
}

func waitPastUnix(t *testing.T, timestamp int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Unix() <= timestamp {
		if time.Now().After(deadline) {
			t.Fatalf("clock did not advance past unix timestamp %d", timestamp)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestThreadsGroupJobsBySession(t *testing.T) {
	s := newWorkbenchTestServer(t, config.ServerConfig{Token: testToken})
	now := time.Now().Unix()
	const firstTitle = "123456789012345678901234567890EXTRA"
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "turn-1", SessionID: "sess-a", Status: job.StatusDone, StartedAt: now - 300}, firstTitle, "first prompt")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "turn-2", SessionID: "sess-a", ResumedFrom: "turn-1", Status: job.StatusDone, StartedAt: now - 200}, "resume title", "second")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "turn-3", SessionID: "sess-a", ResumedFrom: "turn-2", Status: job.StatusRunning, StartedAt: now - 100}, "latest title", "third")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "once-a", Status: job.StatusDone, StartedAt: now - 90}, "one A", "batch A")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "once-b", Status: job.StatusFailed, StartedAt: now - 80}, "one B", "batch B")
	seedWorkbenchRelay(t, s, "relay-a", "self", jobstore.SessionIdle, now-70, now-60)

	got := getWorkbenchThreads(t, s, testToken, "since=1")
	thread := findWorkbenchThread(t, got, "s:sess-a")
	if thread.Kind != "agent" || thread.Turns != 3 || len(thread.JobIDs) != 3 || thread.LatestJobID != "turn-3" {
		t.Fatalf("session thread = %+v, want kind=agent turns=3 latest=turn-3", thread)
	}
	if thread.Title != "123456789012345678901234567890" {
		t.Fatalf("session title = %q, want first title truncated to 30", thread.Title)
	}
	for _, id := range []string{"j:once-a", "j:once-b"} {
		if one := findWorkbenchThread(t, got, id); one.Kind != "job" || one.Turns != 1 {
			t.Fatalf("one-shot %s = %+v", id, one)
		}
	}
	if relay := findWorkbenchThread(t, got, "r:relay-a"); relay.Kind != "relay" {
		t.Fatalf("relay thread kind=%q, want relay", relay.Kind)
	}
}

func TestThreadsStatusPrecedence(t *testing.T) {
	s := newWorkbenchTestServer(t, config.ServerConfig{Token: testToken})
	now := time.Now().Unix()
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "blocked-job", SessionID: "blocked", Status: job.StatusRunning, StartedAt: now - 90}, "blocked", "blocked")
	if err := s.jobs.Meta().UpsertInteraction(jobstore.InteractionRecord{ID: "int-1", JobID: "blocked-job", Type: "confirmation", Prompt: "continue?", Status: "pending", CreatedAt: now - 80}); err != nil {
		t.Fatal(err)
	}
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "working-job", SessionID: "working", Status: job.StatusRunning, StartedAt: now - 70}, "working", "working")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "waiting-job", SessionID: "waiting", Status: job.StatusWaitingDir, StartedAt: now - 60}, "waiting", "waiting")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "review-job", SessionID: "needs-review", Status: job.StatusNeedsReview, StartedAt: now - 50}, "needs review", "review")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "unseen-job", SessionID: "unseen", Status: job.StatusDone, StartedAt: now - 40, UpdatedAt: now - 30}, "unseen", "unseen")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "seen-job", SessionID: "seen", Status: job.StatusDone, StartedAt: now - 35, UpdatedAt: now - 25}, "seen", "seen")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "stalled-job", SessionID: "stalled", Status: job.StatusRunning, StartedAt: now - 20}, "stalled", "stalled")
	if _, err := s.jobs.Meta().InsertJobEvent(jobstore.JobEvent{JobID: "stalled-job", Type: job.EventJobStalled, At: now - 10}); err != nil {
		t.Fatal(err)
	}
	seedWorkbenchRelay(t, s, "relay-wait", "self", jobstore.SessionWaitingReply, now-100, now-5)
	seedWorkbenchRelay(t, s, "relay-idle", "self", jobstore.SessionIdle, now-100, now-4)

	got := getWorkbenchThreads(t, s, testToken, "since=1")
	wants := map[string]string{
		"s:blocked":      "blocked",
		"s:working":      "working",
		"s:waiting":      "working",
		"s:needs-review": "review",
		"s:unseen":       "done",
		"r:relay-wait":   "blocked",
		"r:relay-idle":   "idle",
	}
	for id, want := range wants {
		if gotStatus := findWorkbenchThread(t, got, id).Status; gotStatus != want {
			t.Errorf("thread %s status=%q, want %q", id, gotStatus, want)
		}
	}
	if !findWorkbenchThread(t, got, "s:stalled").Stalled {
		t.Fatal("running thread with job.stalled event has stalled=false")
	}
	patchWorkbenchThread(t, s, testToken, "s:seen", map[string]any{"seen": true})
	if status := findWorkbenchThread(t, getWorkbenchThreads(t, s, testToken, "since=1"), "s:seen").Status; status != "done" {
		t.Fatalf("seen terminal thread status=%q, want done", status)
	}
}

func TestThreadsReviewRequiresChangesOrFailure(t *testing.T) {
	s := newWorkbenchTestServer(t, config.ServerConfig{Token: testToken})
	// The first empty visit establishes the caller baseline before these jobs end.
	_ = getWorkbenchThreads(t, s, testToken, "since=1")
	endedAt := time.Now().Unix() + 10
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "success-clean", SessionID: "success-clean", Status: job.StatusDone, StartedAt: endedAt - 1, UpdatedAt: endedAt}, "clean", "clean")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "success-commits", SessionID: "success-commits", Status: job.StatusDone, StartedAt: endedAt - 1, UpdatedAt: endedAt, CommitsJSON: `[{"sha":"abc123","subject":"change"}]`}, "commits", "commits")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "success-diff", SessionID: "success-diff", Status: job.StatusDone, StartedAt: endedAt - 1, UpdatedAt: endedAt, DiffSummary: " file.go | 1 +"}, "diff", "diff")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "failed", SessionID: "failed", Status: job.StatusFailed, StartedAt: endedAt - 1, UpdatedAt: endedAt}, "failed", "failed")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "cancelled", SessionID: "cancelled", Status: job.StatusCancelled, StartedAt: endedAt - 1, UpdatedAt: endedAt}, "cancelled", "cancelled")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "exec-success", SessionID: "exec-success", Agent: "exec", Status: job.StatusDone, StartedAt: endedAt - 1, UpdatedAt: endedAt, CommitsJSON: `[{"sha":"exec123","subject":"change"}]`, DiffSummary: " file.go | 1 +"}, "exec", "exec")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "needs-review", SessionID: "needs-review-always", Status: job.StatusNeedsReview, StartedAt: endedAt - 1, UpdatedAt: endedAt}, "needs review", "needs review")

	got := getWorkbenchThreads(t, s, testToken, "since=1")
	wants := map[string]string{
		"s:success-clean":       "done",
		"s:success-commits":     "review",
		"s:success-diff":        "review",
		"s:failed":              "review",
		"s:cancelled":           "done",
		"s:exec-success":        "done",
		"s:needs-review-always": "review",
	}
	for id, want := range wants {
		if gotStatus := findWorkbenchThread(t, got, id).Status; gotStatus != want {
			t.Errorf("thread %s status=%q, want %q", id, gotStatus, want)
		}
	}

	patchWorkbenchThread(t, s, testToken, "s:needs-review-always", map[string]any{"seen": true})
	if status := findWorkbenchThread(t, getWorkbenchThreads(t, s, testToken, "since=1"), "s:needs-review-always").Status; status != "review" {
		t.Fatalf("seen needs_review status=%q, want review", status)
	}
}

func TestThreadsSeenBaselineOnFirstVisit(t *testing.T) {
	s := newWorkbenchTestServer(t, config.ServerConfig{Token: testToken})
	now := time.Now().Unix()
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "old-change", SessionID: "old-change", Status: job.StatusDone, StartedAt: now - 200, UpdatedAt: now - 190, CommitsJSON: `[{"sha":"old","subject":"change"}]`}, "old change", "old change")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "old-failure", SessionID: "old-failure", Status: job.StatusFailed, StartedAt: now - 180, UpdatedAt: now - 170}, "old failure", "old failure")

	first := getWorkbenchThreads(t, s, testToken, "since=1")
	for _, id := range []string{"s:old-change", "s:old-failure"} {
		if status := findWorkbenchThread(t, first, id).Status; status != "done" {
			t.Errorf("first-visit historical thread %s status=%q, want done", id, status)
		}
	}

	endedAt := now + 10
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "new-change", SessionID: "new-change", Status: job.StatusDone, StartedAt: endedAt - 1, UpdatedAt: endedAt, CommitsJSON: `[{"sha":"new","subject":"change"}]`}, "new change", "new change")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "new-failure", SessionID: "new-failure", Status: job.StatusFailed, StartedAt: endedAt - 1, UpdatedAt: endedAt}, "new failure", "new failure")
	second := getWorkbenchThreads(t, s, testToken, "since=1")
	for _, id := range []string{"s:new-change", "s:new-failure"} {
		if status := findWorkbenchThread(t, second, id).Status; status != "review" {
			t.Errorf("post-baseline thread %s status=%q, want review", id, status)
		}
	}
}

func TestThreadsSeenAll(t *testing.T) {
	s := newWorkbenchTestServer(t, config.ServerConfig{Token: testToken})
	_ = getWorkbenchThreads(t, s, testToken, "since=1")
	endedAt := time.Now().Unix() + 1
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "seen-all-change", SessionID: "seen-all-change", Status: job.StatusDone, StartedAt: endedAt - 1, UpdatedAt: endedAt, CommitsJSON: `[{"sha":"new","subject":"change"}]`}, "change", "change")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "seen-all-failure", SessionID: "seen-all-failure", Status: job.StatusFailed, StartedAt: endedAt - 1, UpdatedAt: endedAt}, "failure", "failure")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "seen-all-needs-review", SessionID: "seen-all-needs-review", Status: job.StatusNeedsReview, StartedAt: endedAt - 1, UpdatedAt: endedAt}, "needs review", "needs review")

	before := getWorkbenchThreads(t, s, testToken, "since=1")
	for _, id := range []string{"s:seen-all-change", "s:seen-all-failure", "s:seen-all-needs-review"} {
		if status := findWorkbenchThread(t, before, id).Status; status != "review" {
			t.Fatalf("before seen-all thread %s status=%q, want review", id, status)
		}
	}

	waitPastUnix(t, endedAt)
	resp := do(t, s, http.MethodPost, "/v1/workbench/threads/seen-all", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("seen-all status=%d, want 200: %s", resp.StatusCode, body)
	}
	resp.Body.Close()

	after := getWorkbenchThreads(t, s, testToken, "since=1")
	for _, id := range []string{"s:seen-all-change", "s:seen-all-failure"} {
		if status := findWorkbenchThread(t, after, id).Status; status != "done" {
			t.Errorf("after seen-all thread %s status=%q, want done", id, status)
		}
	}
	if status := findWorkbenchThread(t, after, "s:seen-all-needs-review").Status; status != "review" {
		t.Errorf("after seen-all needs_review status=%q, want review", status)
	}
}

func TestThreadAgentUsesOriginAgent(t *testing.T) {
	s := newWorkbenchTestServer(t, config.ServerConfig{Token: testToken})
	now := time.Now().Unix()
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "origin-first", SessionID: "origin-session", Agent: "exec", OriginAgent: "omp", Status: job.StatusDone, StartedAt: now - 20, UpdatedAt: now - 19}, "origin", "origin")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "origin-latest", SessionID: "origin-session", ResumedFrom: "origin-first", Agent: "exec", OriginAgent: "omp", Status: job.StatusRunning, StartedAt: now - 10, UpdatedAt: now - 9}, "resume", "resume")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "first-agent", SessionID: "first-agent-session", Agent: "cli", Status: job.StatusDone, StartedAt: now - 20, UpdatedAt: now - 19}, "first", "first")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "latest-carrier", SessionID: "first-agent-session", ResumedFrom: "first-agent", Agent: "exec", OriginAgent: "cli", Status: job.StatusRunning, StartedAt: now - 10, UpdatedAt: now - 9}, "latest", "latest")

	got := getWorkbenchThreads(t, s, testToken, "since=1")
	if agentID := findWorkbenchThread(t, got, "s:origin-session").Agent; agentID != "omp" {
		t.Errorf("origin carrier thread agent=%q, want omp", agentID)
	}
	if agentID := findWorkbenchThread(t, got, "s:first-agent-session").Agent; agentID != "cli" {
		t.Errorf("first-agent thread agent=%q, want cli", agentID)
	}
}

func TestThreadsAttentionQueueOrder(t *testing.T) {
	s := newWorkbenchTestServer(t, config.ServerConfig{Token: testToken})
	_ = getWorkbenchThreads(t, s, testToken, "since=1")
	now := time.Now().Unix()
	seedWorkbenchRelay(t, s, "relay-old", "self", jobstore.SessionWaitingReply, now-500, now-300)
	decision := jobstore.PlanDecision{Title: "relay", Question: "reply", State: jobstore.DecisionOpen, SessionID: "relay-old", Kind: jobstore.DecisionKindRelay, AskedAt: now - 300, TimeoutSec: 3600}
	if err := s.jobs.Meta().InsertDecision(&decision); err != nil {
		t.Fatal(err)
	}
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "answer-job", SessionID: "answer", Status: job.StatusRunning, StartedAt: now - 250}, "answer", "answer")
	if err := s.jobs.Meta().UpsertInteraction(jobstore.InteractionRecord{ID: "int-old", JobID: "answer-job", Type: "question", Prompt: "answer", Status: "pending", CreatedAt: now - 200}); err != nil {
		t.Fatal(err)
	}
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "review-old", SessionID: "review", Status: job.StatusDone, StartedAt: now - 150, UpdatedAt: now + 10, CommitsJSON: `[{"sha":"review","subject":"change"}]`}, "review", "review")

	attention := getWorkbenchThreads(t, s, testToken, "since=1").Attention
	if len(attention) < 3 {
		t.Fatalf("attention len=%d, want at least 3: %+v", len(attention), attention)
	}
	wantIDs := []string{"r:relay-old", "s:answer", "s:review"}
	wantActions := []string{"reply", "answer", "review"}
	for i := range wantIDs {
		if attention[i].ThreadID != wantIDs[i] || attention[i].Action != wantActions[i] {
			t.Fatalf("attention[%d]=%+v, want thread=%s action=%s", i, attention[i], wantIDs[i], wantActions[i])
		}
		if i > 0 && attention[i-1].WaitingSince > attention[i].WaitingSince {
			t.Fatalf("attention not oldest-first: %+v", attention[:3])
		}
	}
}

func TestThreadsFilterAndRollup(t *testing.T) {
	s := newWorkbenchTestServer(t, config.ServerConfig{Token: testToken})
	now := time.Now().Unix()
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "alpha-work", ProjectKey: "alpha", SessionID: "alpha-work", Status: job.StatusRunning, StartedAt: now - 100}, "needle active", "work")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "alpha-review", ProjectKey: "alpha", SessionID: "alpha-review", Status: job.StatusNeedsReview, StartedAt: now - 90}, "review item", "review")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "beta-done", ProjectKey: "beta", SessionID: "beta-done", Status: job.StatusDone, StartedAt: now - 80, UpdatedAt: now - 70}, "other", "other")
	plan := jobstore.Plan{PlanID: "plan-alpha-001", Title: "alpha plan", ProjectKey: "alpha", Status: jobstore.PlanBlocked, CreatedAt: now - 60, UpdatedAt: now - 50}
	if err := s.jobs.Meta().InsertPlan(plan); err != nil {
		t.Fatal(err)
	}
	decision := jobstore.PlanDecision{PlanID: plan.PlanID, Title: "choose", Question: "which?", State: jobstore.DecisionOpen, AskedAt: now - 40, TimeoutSec: 3600}
	if err := s.jobs.Meta().InsertDecision(&decision); err != nil {
		t.Fatal(err)
	}

	all := getWorkbenchThreads(t, s, testToken, "since=1")
	alpha := findWorkbenchProject(t, all, "alpha")
	if alpha.Status != "blocked" || alpha.Counts.Working != 1 || alpha.Counts.Review != 1 || alpha.Counts.OrphanBlocked != 2 {
		t.Fatalf("alpha rollup=%+v, want blocked with working=1 review=1 orphan_blocked=2", alpha)
	}
	_ = findWorkbenchProject(t, all, "beta")

	query := url.Values{"project": {"alpha"}, "status": {"working"}, "q": {"needle"}, "since": {"1"}}.Encode()
	filtered := getWorkbenchThreads(t, s, testToken, query)
	if len(filtered.Projects) != 1 || len(filtered.Projects[0].Threads) != 1 || filtered.Projects[0].Threads[0].ID != "s:alpha-work" {
		t.Fatalf("filtered response=%+v", filtered)
	}
}

func TestTurnDispatchesByThreadKind(t *testing.T) {
	s := newWorkbenchTestServer(t, config.ServerConfig{Token: testToken})
	now := time.Now().Unix()
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "cli-source", Agent: "cli", SessionID: "cli-session", Status: job.StatusDone, StartedAt: now - 50}, "cli", "first")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "acp-source", Agent: "acp", SessionID: "acp-session", Status: job.StatusDone, StartedAt: now - 40}, "acp", "first")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "one-source", Agent: "cli", Status: job.StatusDone, StartedAt: now - 30}, "one", "batch")
	seedWorkbenchRelay(t, s, "relay-turn", "self", jobstore.SessionWaitingReply, now-100, now-20)
	decision := jobstore.PlanDecision{Title: "relay", Question: "reply", State: jobstore.DecisionOpen, SessionID: "relay-turn", Kind: jobstore.DecisionKindRelay, AskedAt: now - 20, TimeoutSec: 3600}
	if err := s.jobs.Meta().InsertDecision(&decision); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		id string
	}{
		{id: "s:cli-session"},
		{id: "s:acp-session"},
	} {
		resp := do(t, s, http.MethodPost, "/v1/workbench/threads/"+url.PathEscape(tc.id)+"/turn", testToken, map[string]string{"text": "second turn"})
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("turn %s status=%d, want 200: %s", tc.id, resp.StatusCode, body)
		}
		var out workbenchTestTurnResult
		decode(t, resp, &out)
		if out.JobID == "" {
			t.Fatalf("turn %s returned no job id: %+v", tc.id, out)
		}
	}

	resp := do(t, s, http.MethodPost, "/v1/workbench/threads/r:relay-turn/turn", testToken, map[string]string{"text": "relay answer"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("relay turn status=%d, want 200", resp.StatusCode)
	}
	var relayOut workbenchTestTurnResult
	decode(t, resp, &relayOut)
	if relayOut.DecisionID == "" {
		t.Fatalf("relay turn returned no decision id: %+v", relayOut)
	}

	resp = do(t, s, http.MethodPost, "/v1/workbench/threads/j:one-source/turn", testToken, map[string]string{"text": "cannot"})
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(data), "无法续接") {
		t.Fatalf("one-shot turn status=%d body=%s, want 409 with actionable detail", resp.StatusCode, data)
	}
}

func TestThreadPatchRenameAndSeen(t *testing.T) {
	s := newWorkbenchTestServer(t, config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "alice", Token: "tok-alice"},
		{ID: "bob", Token: "tok-bob"},
	}})
	_ = getWorkbenchThreads(t, s, "tok-alice", "since=1")
	now := time.Now().Unix()
	endedAt := now + 1
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "prefs-job", SessionID: "prefs", Status: job.StatusDone, StartedAt: now - 20, UpdatedAt: endedAt, CallerID: "alice", CommitsJSON: `[{"sha":"prefs","subject":"change"}]`}, "default title", "done")

	if before := findWorkbenchThread(t, getWorkbenchThreads(t, s, "tok-alice", "since=1"), "s:prefs"); before.Status != "review" {
		t.Fatalf("before patch status=%q, want review", before.Status)
	}
	waitPastUnix(t, endedAt)
	patchWorkbenchThread(t, s, "tok-alice", "s:prefs", map[string]any{"title": "renamed", "seen": true, "pinned": true})
	alice := findWorkbenchThread(t, getWorkbenchThreads(t, s, "tok-alice", "since=1"), "s:prefs")
	if alice.Title != "renamed" || alice.Status != "done" || !alice.Pinned || alice.SeenAt == 0 {
		t.Fatalf("alice prefs not applied: %+v", alice)
	}
	bob := findWorkbenchThread(t, getWorkbenchThreads(t, s, "tok-bob", "since=1"), "s:prefs")
	if bob.Title != "default title" || bob.Status != "done" || bob.Pinned || bob.SeenAt != 0 {
		t.Fatalf("bob prefs contaminated by alice: %+v", bob)
	}
}

func TestJobCallerCannotTurn(t *testing.T) {
	s := newWorkbenchTestServer(t, config.ServerConfig{Token: testToken})
	now := time.Now().Unix()
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "secure-source", SessionID: "secure", Status: job.StatusDone, StartedAt: now - 10}, "secure", "secure")
	jobToken := seedJobToken(t, s, "caller-job", jobstore.JobCredentialMember, "")
	resp := do(t, s, http.MethodPost, "/v1/workbench/threads/s:secure/turn", jobToken, map[string]string{"text": "forbidden"})
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("job caller turn status=%d, want 403: %s", resp.StatusCode, body)
	}
	assertJobCredentialRefusal(t, resp)
}

func TestWorkbenchValidationAndWorkerWrites(t *testing.T) {
	s := newWorkbenchTestServer(t, config.ServerConfig{
		Token:   testToken,
		Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-worker"}},
	})
	now := time.Now().Unix()
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "valid-source", SessionID: "valid", Status: job.StatusDone, StartedAt: now - 10}, "valid", "valid")

	for _, path := range []string{
		"/v1/workbench/threads?since=bad",
		"/v1/workbench/threads?status=unknown",
	} {
		resp := do(t, s, http.MethodGet, path, testToken, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("GET %s status=%d, want 400", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp := do(t, s, http.MethodPatch, "/v1/workbench/threads/s:valid", testToken, map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty patch status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/workbench/threads/not-a-thread/turn", testToken, map[string]string{"text": "x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid thread id status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodPatch, "/v1/workbench/threads/s:missing", testToken, map[string]any{"seen": true})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown patch status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	for _, target := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPatch, "/v1/workbench/threads/s:valid", map[string]any{"seen": true}},
		{http.MethodPost, "/v1/workbench/threads/seen-all", nil},
		{http.MethodPost, "/v1/workbench/threads/s:valid/turn", map[string]string{"text": "x"}},
	} {
		resp = do(t, s, target.method, target.path, "tok-worker", target.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("worker %s %s status=%d, want 403", target.method, target.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}
