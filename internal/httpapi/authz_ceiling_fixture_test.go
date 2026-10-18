package httpapi

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// Fixtures of the caller-ceiling tests (plan S4b): one world of resources per project, a
// server with a restricted caller, a restricted read-only caller and an unrestricted one.

const (
	ceilTokLim   = "tok-lim"   // ceiling: projects [p1], workers [w-ok]
	ceilTokRO    = "tok-ro"    // ceiling: projects [p1], read_only
	ceilTokPlain = "tok-plain" // no ceiling
	ceilOwner    = "bob"       // the caller that owns every fixture resource
)

// ceilWorld is the set of resources of ONE project, owned by ceilOwner.
type ceilWorld struct {
	project string

	jobID, planID, todoID, scheduleID, workflowID string
	sessionID, workItemID, workItem2ID            string
	trackerID, issueID                            string
	xferID, wakeupID, retryID, decisionID         string
	nudgeID                                       string
	mergeSugID, candidateID, suggestionID         string
}

// allPermissionNames lists every permission of the catalog: a caller granted all of them is
// stopped only by its ceiling, which is what the matrix tests want to prove.
func allPermissionNames() []string {
	var out []string
	for _, p := range authz.Permissions() {
		out = append(out, p.Name)
	}
	return out
}

// newCeilingServer builds a server with three callers — lim (ceiling p1, workers [w-ok]),
// ro (ceiling p1, read_only) and plain (no ceiling) — over projects p1 and p2. Governance gates are
// closed so the config derives no permission; the tests grant what they need (grantAll).
func newCeilingServer(t *testing.T) *Server {
	t.Helper()
	s := recordingServerWith(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "lim", Token: ceilTokLim, Ceiling: &config.CallerCeiling{Projects: []string{"p1"}, Workers: []string{"w-ok"}}},
			{ID: "ro", Token: ceilTokRO, Ceiling: &config.CallerCeiling{Projects: []string{"p1"}, ReadOnly: boolptr(true)}},
			{ID: "plain", Token: ceilTokPlain},
		},
		Governance: config.GovernanceConfig{
			RequireAnswerCapability: true, RequireAdminCapability: true,
			RequireAttachCapability: true, RequireTunnelCapability: true,
		},
	}, func(cfg *config.Config) {
		for _, p := range []string{"p1", "p2"} {
			cfg.Projects[p] = config.ProjectConfig{
				HostPath: cfg.Storage.Root, AllowedAgents: []string{"exec"},
				AllowedRunners: []string{"local"}, AllowExec: true,
			}
		}
	})
	s.SetTrackerStore(s.jobs.Meta())
	setTransferManager(t, s)
	return s
}

// grantAll gives every user-class caller the whole permission catalog (the database half).
func grantAll(s *Server) { s.setDBGrants(grantsFor(allPermissionNames())) }

// seedCeilingWorld creates one resource of every kind in `project`.
func seedCeilingWorld(t *testing.T, s *Server, project string) ceilWorld {
	t.Helper()
	w := ceilWorld{project: project}
	st := s.jobs.Meta()
	now := time.Now().Unix()
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed %s (%s): %v", what, project, err)
		}
	}
	id := func(kind string) string { return kind + "-" + project }

	w.jobID = id("job")
	must("job", st.UpsertJob(jobstore.JobRecord{
		ID: w.jobID, ProjectKey: project, Agent: "exec", Runner: "worker", Status: job.StatusDone,
		Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, EndedAt: now, UpdatedAt: now, CallerID: ceilOwner,
	}))

	w.planID = id("plan")
	must("plan", st.InsertPlan(jobstore.Plan{
		PlanID: w.planID, Title: "plan " + project, Status: jobstore.PlanOpen, Owner: ceilOwner,
		ProjectKey: project, CreatedAt: now, UpdatedAt: now,
	}))
	w.todoID = id("todo")
	must("todo", st.InsertTodo(jobstore.PlanTodo{
		TodoID: w.todoID, PlanID: w.planID, Title: "todo", Status: jobstore.TodoPending,
		Auto: true, CreatedAt: now, UpdatedAt: now,
	}))

	w.scheduleID = id("sch")
	raw, err := json.Marshal(job.JobRequest{
		CallerID: ceilOwner, ProjectKey: project, Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	must("schedule request", err)
	must("schedule", st.InsertSchedule(jobstore.ScheduleRecord{
		ID: w.scheduleID, Name: "sch " + project, ScheduleType: "cron", CronExpr: "*/5 * * * *",
		RequestJSON: string(raw), Enabled: 1, NextRunAt: now + 300, CatchUp: 1,
		ProjectKey: project, TriggerToken: "tok-" + project, CreatedAt: now, UpdatedAt: now,
	}))

	w.workflowID = id("wf")
	must("workflow", st.InsertWorkflow(jobstore.Workflow{
		ID: w.workflowID, Status: jobstore.WorkflowRunning, CallerID: ceilOwner, CurrentStep: 1, TotalSteps: 1,
		SpecJSON:  fmt.Sprintf(`{"steps":[{"name":"a","project_key":%q,"agent":"exec"}]}`, project),
		CreatedAt: now, UpdatedAt: now,
	}))

	w.sessionID = id("sess")
	_, err = st.UpsertAgentSession(jobstore.AgentSession{
		SessionID: w.sessionID, Agent: "claude", ProjectKey: project, Runner: "local", Cwd: ".",
		CallerID: ceilOwner, State: jobstore.SessionIdle, StartedAt: now, LastSeenAt: now,
	})
	must("session", err)
	n, err := s.relay.CreateNudge(w.sessionID, sessionrelay.NudgeInput{
		Kind: jobstore.NudgeEvery, Interval: time.Hour, Text: "ping", By: ceilOwner,
	})
	must("nudge", err)
	w.nudgeID = n.ID

	wi, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "item " + project, ProjectKey: project, By: ceilOwner})
	must("work item", err)
	w.workItemID = wi.ID
	wi2, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "item2 " + project, ProjectKey: project, By: ceilOwner})
	must("work item 2", err)
	w.workItem2ID = wi2.ID
	sug, _, err := st.AddWorkMergeSuggestion(w.workItemID, w.workItem2ID, "dup", ceilOwner)
	must("merge suggestion", err)
	w.mergeSugID = fmt.Sprint(sug.ID)

	w.trackerID = id("trk")
	must("tracker repo", st.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: w.trackerID, ProjectKey: project, RelPath: "."}))
	w.issueID = "ISS-" + project
	body, _ := json.Marshal(map[string]any{"id": w.issueID, "title": "issue " + project, "status": "open"})
	must("tracker issue", st.UpsertTrackerIssue(jobstore.TrackerRecord{
		TrackerID: w.trackerID, ID: w.issueID, Body: body, Rev: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}))
	_, err = st.PutScopedMemory("project", project, "mem-key", "content "+project, nil, ceilOwner)
	must("project memory", err)

	w.xferID = id("xf")
	must("xfer", st.InsertXfer(jobstore.XferRecord{
		ID: w.xferID, Op: "put", Runner: "local", ProjectKey: project, Path: "tmp/" + w.xferID,
		Size: 3, SHA256: "abc", State: "done", CallerID: ceilOwner, CreatedAt: now,
	}))

	wk, err := s.jobs.CreateWakeup(job.WakeupSpec{
		JobID: w.jobID, Kind: jobstore.WakeupKindAt, At: now + 3600, Instruction: "test",
	}, ceilOwner, job.AllowAllWakeups)
	must("wakeup", err)
	w.wakeupID = wk.ID
	w.retryID = id("rt")
	must("retry", st.InsertRetry(jobstore.RetryRecord{
		ID: w.retryID, SourceJobID: w.jobID, Attempt: 1, Reason: "test", RequestJSON: "{}",
		NextRunAt: now + 3600, State: jobstore.RetryPending, CreatedAt: now,
	}))

	d := jobstore.PlanDecision{PlanID: w.planID, Title: "pick", Question: "which?"}
	must("decision", st.InsertDecision(&d))
	w.decisionID = d.ID

	_, err = st.AddMemoryCandidates(w.jobID, project, []string{"candidate " + project}, nil)
	must("memory candidate", err)
	cands, err := st.ListMemoryCandidates(jobstore.MemoryCandidateFilter{ProjectKey: project})
	must("list memory candidates", err)
	if len(cands) == 0 {
		t.Fatalf("seed memory candidate (%s): none listed", project)
	}
	w.candidateID = fmt.Sprint(cands[0].ID)
	ms, _, err := st.AddMemorySuggestion(jobstore.MemorySuggestion{
		TrackerID: w.trackerID, MemoryKey: "mem-key", Action: "archive", By: ceilOwner,
	})
	must("memory suggestion", err)
	w.suggestionID = fmt.Sprint(ms.ID)
	return w
}

// ids lists every id of the world that a response body must not reveal to a caller outside
// the project.
func (w ceilWorld) ids() []string {
	return []string{
		w.jobID, w.planID, w.todoID, w.scheduleID, w.workflowID, w.sessionID, w.workItemID, w.workItem2ID,
		w.trackerID, w.issueID, w.xferID, w.wakeupID, w.retryID, w.nudgeID,
		"item " + w.project, "plan " + w.project, "sch " + w.project, "issue " + w.project,
		"content " + w.project, "candidate " + w.project,
	}
}

// ceilReq sends a request with a raw token and returns status plus body.
func ceilReq(t *testing.T, s *Server, token, method, path string, body any) (int, string) {
	t.Helper()
	resp := do(t, s, method, path, token, body)
	return resp.StatusCode, readBody(t, resp)
}

// notFoundShape decodes a refusal body and checks it is the "not found" shape.
func notFoundShape(t *testing.T, body string) map[string]string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("refusal body is not the error shape: %q (%v)", body, err)
	}
	if m["error"] != "not found" {
		t.Fatalf("refusal body = %q, want error=\"not found\"", body)
	}
	return m
}
