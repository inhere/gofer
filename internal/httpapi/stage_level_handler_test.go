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
