package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

type stageViewBody struct {
	WorkItemID string   `json:"work_item_id"`
	Template   string   `json:"template"`
	Steps      []string `json:"steps"`
	Step       string   `json:"step"`
	Status     string   `json:"status"`
	Blocked    string   `json:"blocked_reason"`
	Gate       *struct {
		Step       string   `json:"step"`
		OK         bool     `json:"ok"`
		Missing    []string `json:"missing"`
		NeedsHuman bool     `json:"needs_human"`
	} `json:"gate"`
}

type stageResultBody struct {
	Advanced bool          `json:"advanced"`
	Moved    bool          `json:"moved"`
	Stage    stageViewBody `json:"stage"`
	Gate     struct {
		OK         bool     `json:"ok"`
		Missing    []string `json:"missing"`
		NeedsHuman bool     `json:"needs_human"`
	} `json:"gate"`
}

// stageDo is do() with extra request headers.
func stageDo(t *testing.T, s *Server, method, path, token string, body any, hdr map[string]string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result()
}

func stageCall(t *testing.T, s *Server, method, path string, body any, want int, out any) {
	t.Helper()
	resp := do(t, s, method, path, testToken, body)
	if resp.StatusCode != want {
		var raw map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&raw)
		t.Fatalf("%s %s = %d, want %d: %v", method, path, resp.StatusCode, want, raw)
	}
	if out != nil {
		decode(t, resp, out)
	} else {
		resp.Body.Close()
	}
}

func TestStageFlowOverHTTP(t *testing.T) {
	s := newTestServer(t, testToken, false)

	var v stageViewBody
	stageCall(t, s, http.MethodPost, "/v1/work-items/stage", map[string]any{"issue": "gofer-5", "template": "small", "project": "self", "title": "fix"}, http.StatusCreated, &v)
	if v.Template != "small" || v.Step != "doing" || v.Status != "active" || v.WorkItemID == "" {
		t.Fatalf("start = %+v", v)
	}
	base := "/v1/work-items/" + v.WorkItemID + "/stage"

	// The same issue again finds the item and is refused: a flow is already in progress.
	stageCall(t, s, http.MethodPost, "/v1/work-items/stage", map[string]any{"issue": "gofer-5", "project": "self"}, http.StatusConflict, nil)
	stageCall(t, s, http.MethodPost, base, map[string]any{}, http.StatusConflict, nil)

	var got stageViewBody
	stageCall(t, s, http.MethodGet, base, nil, http.StatusOK, &got)
	if got.Gate == nil || !got.Gate.OK || got.Gate.Step != "doing" || got.Gate.Missing == nil {
		t.Fatalf("status gate = %+v", got.Gate)
	}

	// doing has no plan to wait for: done advances to verify.
	var res stageResultBody
	stageCall(t, s, http.MethodPost, base+"/done", nil, http.StatusOK, &res)
	if !res.Advanced || res.Stage.Step != "verify" {
		t.Fatalf("done = %+v", res)
	}
	// verify cannot be judged here (the project root is not a git repository): the flow stays,
	// the answer is not an error but needs_human with what is missing.
	stageCall(t, s, http.MethodPost, base+"/done", nil, http.StatusOK, &res)
	if res.Advanced || !res.Gate.NeedsHuman || len(res.Gate.Missing) == 0 || res.Stage.Step != "verify" {
		t.Fatalf("done at verify = %+v", res)
	}

	// back needs a reason; blocked likewise.
	stageCall(t, s, http.MethodPost, base+"/back", map[string]any{}, http.StatusBadRequest, nil)
	stageCall(t, s, http.MethodPost, base+"/blocked", map[string]any{"reason": ""}, http.StatusBadRequest, nil)
	stageCall(t, s, http.MethodPost, base+"/blocked", map[string]any{"reason": "waiting for the lab"}, http.StatusOK, &v)
	if v.Status != "blocked" || v.Blocked != "waiting for the lab" {
		t.Fatalf("blocked = %+v", v)
	}
	stageCall(t, s, http.MethodPost, base+"/back", map[string]any{"reason": "found a bug"}, http.StatusOK, &v)
	if v.Step != "doing" || v.Status != "active" {
		t.Fatalf("back = %+v", v)
	}
	stageCall(t, s, http.MethodPost, base+"/done", nil, http.StatusOK, &res)
	if !res.Advanced {
		t.Fatalf("done after back = %+v", res)
	}

	// A person approves the verification that could not be judged: the flow is done.
	stageCall(t, s, http.MethodPost, base+"/approve", nil, http.StatusOK, &res)
	if !res.Advanced || res.Stage.Status != "done" || res.Stage.Step != "done" {
		t.Fatalf("approve = %+v", res)
	}
	stageCall(t, s, http.MethodPost, base+"/done", nil, http.StatusConflict, nil)
	stageCall(t, s, http.MethodPost, base+"/drop", map[string]any{}, http.StatusConflict, nil)

	// The journal carries the flow, kind stage.
	item := workGet(t, s, "/v1/work-items/"+v.WorkItemID)
	n := 0
	for _, j := range item.Journal {
		if j.Kind == jobstore.WorkJournalStage {
			n++
		}
	}
	if n < 5 {
		t.Fatalf("stage journal lines = %d, want at least 5 (start, steps, back, blocked, approve)", n)
	}
}

func TestStageDropAndErrors(t *testing.T) {
	s := newTestServer(t, testToken, false)
	stageCall(t, s, http.MethodGet, "/v1/work-items/w-nope/stage", nil, http.StatusNotFound, nil)
	stageCall(t, s, http.MethodPost, "/v1/work-items/w-nope/stage/done", nil, http.StatusNotFound, nil)
	stageCall(t, s, http.MethodPost, "/v1/work-items/stage", map[string]any{"project": "self"}, http.StatusBadRequest, nil)
	stageCall(t, s, http.MethodPost, "/v1/work-items/stage", map[string]any{"issue": "x-1", "template": "huge", "project": "self"}, http.StatusBadRequest, nil)

	it := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "plain", "project_key": "self"}, 200)
	stageCall(t, s, http.MethodGet, "/v1/work-items/"+it.ID+"/stage", nil, http.StatusNotFound, nil)

	var v stageViewBody
	stageCall(t, s, http.MethodPost, "/v1/work-items/"+it.ID+"/stage", map[string]any{"template": "feature"}, http.StatusCreated, &v)
	if v.Step != "design" || len(v.Steps) != 6 {
		t.Fatalf("start on an item = %+v", v)
	}
	stageCall(t, s, http.MethodPost, "/v1/work-items/"+it.ID+"/stage/drop", map[string]any{"reason": "changed my mind"}, http.StatusOK, &v)
	if v.Status != "dropped" {
		t.Fatalf("drop = %+v", v)
	}
}

func TestStageApproveIsOnlyForAPerson(t *testing.T) {
	s := newTestServer(t, testToken, false)
	registerSessionInProject(t, s, "sess-stage-aaaa", "/ws/a", "self")
	var v stageViewBody
	stageCall(t, s, http.MethodPost, "/v1/work-items/stage", map[string]any{"issue": "gofer-6", "template": "feature", "project": "self"}, http.StatusCreated, &v)
	path := "/v1/work-items/" + v.WorkItemID + "/stage/approve"

	// A request that names a registered live session is the agent, not the person.
	resp := stageDo(t, s, http.MethodPost, path, testToken, nil, map[string]string{sessionHeader: "sess-stage-aaaa"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("approve from a session = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// A job credential cannot approve nor drop the flow, but it may move its OWN project's
	// flow (`done` / `back` / `blocked` / start — F2): here that is the design gate, which
	// does not pass yet, so nothing is written.
	jobTok := seedJobToken(t, s, "job-stage", jobstore.JobCredentialMember, "")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "job-stage", Status: job.StatusRunning, StartedAt: time.Now().Unix()}, "stage job", "")
	for _, p := range []string{path, "/v1/work-items/" + v.WorkItemID + "/stage/drop"} {
		resp = stageDo(t, s, http.MethodPost, p, jobTok, map[string]any{"issue": "gofer-6"}, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("job credential POST %s = %d, want 403", p, resp.StatusCode)
		}
		resp.Body.Close()
	}
	var jobRes stageResultBody
	resp = stageDo(t, s, http.MethodPost, "/v1/work-items/"+v.WorkItemID+"/stage/done", jobTok, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("job credential done = %d, want 200", resp.StatusCode)
	}
	decode(t, resp, &jobRes)
	if jobRes.Advanced || jobRes.Stage.Step != "design" {
		t.Fatalf("job credential done = %+v, want the flow kept at design", jobRes)
	}
	resp = stageDo(t, s, http.MethodGet, "/v1/work-items/"+v.WorkItemID+"/stage", jobTok, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("job credential GET stage = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// The flow did not move; a person's own request does approve (design cannot be waived
	// while its doc is missing: recorded, not advanced).
	var res stageResultBody
	stageCall(t, s, http.MethodPost, path, nil, http.StatusOK, &res)
	if res.Advanced || res.Stage.Step != "design" {
		t.Fatalf("approve of an unmet design gate = %+v", res)
	}
}

func TestStageStartBindsTheCallingSession(t *testing.T) {
	s := newTestServer(t, testToken, false)
	registerSessionInProject(t, s, "sess-stage-bbbb", "/ws/a", "self")
	resp := stageDo(t, s, http.MethodPost, "/v1/work-items/stage", testToken,
		map[string]any{"issue": "gofer-11", "template": "small", "project": "self"}, map[string]string{sessionHeader: "sess-stage-bbbb"})
	var v stageViewBody
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start = %d", resp.StatusCode)
	}
	decode(t, resp, &v)
	item := workGet(t, s, "/v1/work-items/"+v.WorkItemID)
	if len(item.Sessions) != 1 || item.Sessions[0].SessionID != "sess-stage-bbbb" || item.Sessions[0].Role != "current" {
		t.Fatalf("sessions = %+v", item.Sessions)
	}
	// The journal names the session as the actor.
	found := false
	for _, j := range item.Journal {
		if j.Kind == jobstore.WorkJournalStage && j.By == "session:sess-stage-bbbb" {
			found = true
		}
	}
	if !found {
		t.Fatalf("stage start must be journaled as the session's: %+v", item.Journal)
	}
}

func TestWorkLinksCarryDocRoles(t *testing.T) {
	s := newTestServer(t, testToken, false)
	it := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "docs", "project_key": "self"}, 200)
	path := "/v1/work-items/" + it.ID + "/links"
	workCall(t, s, http.MethodPost, path, testToken, map[string]any{"kind": "doc", "ref": "docs/design/x.md", "role": "design"}, 200)
	workCall(t, s, http.MethodPost, path, testToken, map[string]any{"kind": "doc", "ref": "docs/design/y.md", "role": "bogus"}, http.StatusBadRequest)
	workCall(t, s, http.MethodPost, path, testToken, map[string]any{"kind": "doc", "ref": "docs/design/z.md"}, http.StatusBadRequest)
	workCall(t, s, http.MethodPost, path, testToken, map[string]any{"kind": "issue", "ref": "gofer-2"}, 200)

	var out struct {
		Links []struct{ Kind, Ref, Role string } `json:"links"`
	}
	stageCall(t, s, http.MethodGet, path, nil, http.StatusOK, &out)
	roles := map[string]string{}
	for _, l := range out.Links {
		roles[l.Kind+":"+l.Ref] = l.Role
	}
	if roles["doc:docs/design/x.md"] != "design" || len(out.Links) != 2 {
		t.Fatalf("links = %+v", out.Links)
	}
}

// TestStageOpenListAndJobScope (F2): GET /v1/work-items/stages lists the flows in progress
// of one project, and a job credential reaches only its OWN project's flow — start / done /
// back / blocked inside it, nothing on another project's item.
func TestStageOpenListAndJobScope(t *testing.T) {
	s := newTestServerCfgWith(t, config.ServerConfig{Token: testToken}, func(cfg *config.Config) {
		self := cfg.Projects["self"]
		cfg.Projects["other"] = self
	})
	var v stageViewBody
	stageCall(t, s, http.MethodPost, "/v1/work-items/stage", map[string]any{"issue": "gofer-21", "project": "self"}, http.StatusCreated, &v)

	var list struct {
		Stages []stageViewBody `json:"stages"`
	}
	stageCall(t, s, http.MethodGet, "/v1/work-items/stages?project=self", nil, http.StatusOK, &list)
	if len(list.Stages) != 1 || list.Stages[0].WorkItemID != v.WorkItemID || list.Stages[0].Step != "design" {
		t.Fatalf("open flows = %+v", list.Stages)
	}
	stageCall(t, s, http.MethodGet, "/v1/work-items/stages?project=other", nil, http.StatusOK, &list)
	if len(list.Stages) != 0 {
		t.Fatalf("other project's open flows = %+v, want none", list.Stages)
	}

	// A job of ANOTHER project cannot move or start this project's flow.
	otherTok := seedJobToken(t, s, "job-other", jobstore.JobCredentialMember, "")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "job-other", ProjectKey: "other", Status: job.StatusRunning, StartedAt: time.Now().Unix()}, "other job", "")
	for _, path := range []string{"/v1/work-items/" + v.WorkItemID + "/stage", "/v1/work-items/" + v.WorkItemID + "/stage/done",
		"/v1/work-items/" + v.WorkItemID + "/stage/back", "/v1/work-items/" + v.WorkItemID + "/stage/blocked"} {
		resp := stageDo(t, s, http.MethodPost, path, otherTok, map[string]any{"reason": "x", "issue": "gofer-21"}, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("job of another project POST %s = %d, want 403", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp := stageDo(t, s, http.MethodPost, "/v1/work-items/stage", otherTok, map[string]any{"issue": "gofer-22", "project": "self"}, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("job of another project start in self = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// Its own project is open to it: start, blocked, then done (which unblocks it).
	resp = stageDo(t, s, http.MethodPost, "/v1/work-items/stage", otherTok, map[string]any{"issue": "gofer-23", "project": "other", "template": "small"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("job start in its own project = %d, want 201", resp.StatusCode)
	}
	var own stageViewBody
	decode(t, resp, &own)
	// The job sent no session id: the job itself becomes the item's session, so the per-turn
	// stage line (looked up by job id) reaches that resident session.
	sessions, err := s.jobs.Meta().ListWorkItemSessions(own.WorkItemID)
	if err != nil || len(sessions) != 1 || sessions[0].SessionID != "job-other" {
		t.Fatalf("sessions of the job-started item = %+v, %v, want job-other", sessions, err)
	}
	resp = stageDo(t, s, http.MethodPost, "/v1/work-items/"+own.WorkItemID+"/stage/blocked", otherTok, map[string]any{"reason": "waiting for the review"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("job blocked = %d, want 200", resp.StatusCode)
	}
	var blocked stageViewBody
	decode(t, resp, &blocked)
	if blocked.Status != "blocked" || blocked.Blocked != "waiting for the review" {
		t.Fatalf("job blocked = %+v", blocked)
	}
	resp.Body.Close()
	resp = stageDo(t, s, http.MethodPost, "/v1/work-items/"+own.WorkItemID+"/stage/back", otherTok, map[string]any{"reason": "start over"}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("job back at the first step = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}
