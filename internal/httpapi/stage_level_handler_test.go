package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/stage"
)

func TestStageLevelIsOnlyForAPerson(t *testing.T) {
	s := newTestServer(t, testToken, false)
	registerSessionInProject(t, s, "sess-level-aaaa", "/ws/a", "self")
	var v stageViewBody
	stageCall(t, s, http.MethodPost, "/v1/work-items/stage", map[string]any{"issue": "gofer-21", "template": "small", "project": "self"}, http.StatusCreated, &v)
	path := "/v1/work-items/" + v.WorkItemID + "/stage/level"

	// The agent in a registered session cannot widen its own leash.
	resp := stageDo(t, s, http.MethodPost, path, testToken, map[string]any{"level": "remind"}, map[string]string{sessionHeader: "sess-level-aaaa"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("level from a session = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
	// Neither can a job credential.
	jobTok := seedJobToken(t, s, "job-level", jobstore.JobCredentialMember, "")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "job-level", Status: job.StatusRunning, StartedAt: time.Now().Unix()}, "level job", "")
	resp = stageDo(t, s, http.MethodPost, path, jobTok, map[string]any{"level": "remind"}, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("level from a job credential = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
	var got stage.View
	stageCall(t, s, http.MethodGet, "/v1/work-items/"+v.WorkItemID+"/stage", nil, http.StatusOK, &got)
	if got.Level != "record" {
		t.Fatalf("level after the refusals = %q, want record", got.Level)
	}

	// A person sets it; a value outside the two levels is a 400.
	stageCall(t, s, http.MethodPost, path, map[string]any{"level": "auto"}, http.StatusBadRequest, nil)
	stageCall(t, s, http.MethodPost, path, map[string]any{"level": "remind"}, http.StatusOK, &got)
	if got.Level != "remind" {
		t.Fatalf("level = %q, want remind", got.Level)
	}
	stageCall(t, s, http.MethodPost, "/v1/work-items/w-nope/stage/level", map[string]any{"level": "record"}, http.StatusNotFound, nil)
}

func TestStageReportOverHTTP(t *testing.T) {
	s := newTestServer(t, testToken, false)
	var v stageViewBody
	stageCall(t, s, http.MethodPost, "/v1/work-items/stage", map[string]any{"issue": "gofer-22", "template": "small", "project": "self"}, http.StatusCreated, &v)
	base := "/v1/work-items/" + v.WorkItemID + "/stage"
	var rep stage.Report
	stageCall(t, s, http.MethodGet, base+"/report", nil, http.StatusOK, &rep)
	if rep.Total != 0 || rep.WorkItemID != v.WorkItemID {
		t.Fatalf("report before any step change = %+v", rep)
	}
	stageCall(t, s, http.MethodPost, base+"/done", nil, http.StatusOK, nil)
	stageCall(t, s, http.MethodGet, base+"/report", nil, http.StatusOK, &rep)
	if rep.Total != 1 || rep.Pending != 1 || len(rep.ByAction) != 1 || rep.ByAction[0].Action != stage.ActionSendContinue {
		t.Fatalf("item report = %+v", rep)
	}
	stageCall(t, s, http.MethodGet, "/v1/work-items/stages/report?project=self", nil, http.StatusOK, &rep)
	if rep.Total != 1 || rep.ProjectKey != "self" {
		t.Fatalf("project report = %+v", rep)
	}
	stageCall(t, s, http.MethodGet, "/v1/work-items/stages/report", nil, http.StatusOK, &rep)
	if rep.Total != 1 {
		t.Fatalf("server-wide report = %+v", rep)
	}
	stageCall(t, s, http.MethodGet, "/v1/work-items/w-nope/stage/report", nil, http.StatusNotFound, nil)
}

type stageWaitingBody struct {
	Waiting []struct {
		Key     string `json:"key"`
		Kind    string `json:"kind"`
		Subtype string `json:"subtype"`
		Waiting string `json:"waiting"`
		Refs    struct {
			WorkItemID string `json:"work_item_id"`
		} `json:"refs"`
		Actions []struct {
			ID        string `json:"id"`
			NeedsText bool   `json:"needs_text"`
		} `json:"actions"`
	} `json:"waiting"`
}

func stageCardOf(t *testing.T, s *Server, workID string) (kind, subtype, class string, actions []string, found bool) {
	t.Helper()
	var got stageWaitingBody
	todayCall(t, s, http.MethodGet, "/v1/today", testToken, nil, http.StatusOK, &got)
	for _, c := range got.Waiting {
		if c.Kind == "stage" && c.Refs.WorkItemID == workID {
			for _, a := range c.Actions {
				actions = append(actions, a.ID)
			}
			return c.Kind, c.Subtype, c.Waiting, actions, true
		}
	}
	return "", "", "", nil, false
}

// A flow that reaches review shows up in 「等我」 as an approval card; approving it from the
// card's route removes it, and sending the flow back puts it on the previous step with the
// card gone.
func TestStageReviewCardLifecycleOverHTTP(t *testing.T) {
	s := newTestServer(t, testToken, false)
	var v stageViewBody
	stageCall(t, s, http.MethodPost, "/v1/work-items/stage", map[string]any{"issue": "gofer-31", "template": "feature", "project": "self", "title": "login"}, http.StatusCreated, &v)
	base := "/v1/work-items/" + v.WorkItemID + "/stage"
	if _, _, _, _, found := stageCardOf(t, s, v.WorkItemID); found {
		t.Fatal("a flow at design waits for nobody")
	}
	moveStage(t, s, v.WorkItemID, 1) // design -> review
	_, sub, class, actions, found := stageCardOf(t, s, v.WorkItemID)
	if !found || sub != "review" || class != "approval" || len(actions) != 3 || actions[0] != "stage_approve" || actions[1] != "stage_back" {
		t.Fatalf("review card = %v %q %q %v", found, sub, class, actions)
	}

	// Back with a note (the card's stage_back action): the flow returns to design, the card goes.
	stageCall(t, s, http.MethodPost, base+"/back", map[string]any{"reason": "needs a threat model"}, http.StatusOK, &v)
	if v.Step != "design" {
		t.Fatalf("back = %+v", v)
	}
	if _, _, _, _, found := stageCardOf(t, s, v.WorkItemID); found {
		t.Fatal("the card must go once the flow is back at design")
	}

	// Review again, then the card's stage_approve: the flow moves to plan and the card goes.
	moveStage(t, s, v.WorkItemID, 1)
	if _, _, _, _, found := stageCardOf(t, s, v.WorkItemID); !found {
		t.Fatal("review card missing the second time")
	}
	var res stageResultBody
	stageCall(t, s, http.MethodPost, base+"/approve", nil, http.StatusOK, &res)
	if !res.Advanced || res.Stage.Step != "plan" {
		t.Fatalf("approve = %+v", res)
	}
	if _, _, _, _, found := stageCardOf(t, s, v.WorkItemID); found {
		t.Fatal("the card must go once approved")
	}
}

// moveStage moves the flow to step index to straight in the store (a gate would otherwise
// want documents).
func moveStage(t *testing.T, s *Server, workID string, to int) {
	t.Helper()
	st := s.jobs.Meta()
	ws, ok, err := st.GetWorkStage(workID)
	if err != nil || !ok {
		t.Fatalf("stage: %v %v", ok, err)
	}
	if applied, err := st.TransitionWorkStage(jobstore.StageTransition{WorkItemID: workID, FromStep: ws.Step, ToStep: to, ToStatus: jobstore.StageActive, Journal: "test move"}); err != nil || !applied {
		t.Fatalf("move: %v %v", applied, err)
	}
}

// A step whose gate nobody can judge here is an attention card with approve and back.
func TestStageNeedsHumanCardOverHTTP(t *testing.T) {
	s := newTestServer(t, testToken, false)
	var v stageViewBody
	stageCall(t, s, http.MethodPost, "/v1/work-items/stage", map[string]any{"issue": "gofer-32", "template": "small", "project": "self", "title": "export"}, http.StatusCreated, &v)
	stageCall(t, s, http.MethodPost, "/v1/work-items/"+v.WorkItemID+"/stage/done", nil, http.StatusOK, nil) // -> verify, not git here
	_, sub, class, actions, found := stageCardOf(t, s, v.WorkItemID)
	if !found || sub != "needs_human" || class != "attention" || len(actions) != 3 {
		t.Fatalf("needs_human card = %v %q %q %v", found, sub, class, actions)
	}
	stageCall(t, s, http.MethodPost, "/v1/work-items/"+v.WorkItemID+"/stage/approve", nil, http.StatusOK, nil)
	if _, _, _, _, found := stageCardOf(t, s, v.WorkItemID); found {
		t.Fatal("the card must go once confirmed")
	}
}

// The review card goes through the same notice record as the session cards (waitnotice): the
// source lists it with its class and the work item's title.
func TestStageCardReachesTheWaitingNoticeSource(t *testing.T) {
	s := newTestServer(t, testToken, false)
	var v stageViewBody
	stageCall(t, s, http.MethodPost, "/v1/work-items/stage", map[string]any{"issue": "gofer-33", "template": "feature", "project": "self", "title": "billing"}, http.StatusCreated, &v)
	moveStage(t, s, v.WorkItemID, 1)
	subs, err := waitingNoticeSource{s}.Waiting()
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Key != "stage:"+v.WorkItemID || subs[0].Category != "approval" || subs[0].Title != "billing" || subs[0].ProjectKey != "self" {
		t.Fatalf("notice subjects = %+v", subs)
	}
	stageCall(t, s, http.MethodPost, "/v1/work-items/"+v.WorkItemID+"/stage/approve", nil, http.StatusOK, nil)
	if subs, err := (waitingNoticeSource{s}).Waiting(); err != nil || len(subs) != 0 {
		t.Fatalf("after the approval: %+v %v", subs, err)
	}
}
