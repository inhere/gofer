package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// seatRosterByName is GET /v1/seats decoded by seat name.
func seatRosterByName(t *testing.T, s *Server) map[string]seatView {
	t.Helper()
	status, resp := seatReq(t, s, ceilTokPlain, "", http.MethodGet, "/v1/seats", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/seats = %d %s", status, resp)
	}
	var body struct {
		Seats []seatView `json:"seats"`
	}
	if err := json.Unmarshal([]byte(resp), &body); err != nil {
		t.Fatalf("decode roster %q: %v", resp, err)
	}
	out := make(map[string]seatView, len(body.Seats))
	for _, v := range body.Seats {
		out[v.Name] = v
	}
	if strings.Count(resp, `"stage"`) != 1 {
		t.Fatalf("exactly the seat whose session has a flow in progress carries stage: %s", resp)
	}
	return out
}

// The roster row says where the flow of the sitting session stands: the seat whose session is
// on a flow in progress carries `stage` (the same brief the session view gives), a seat whose
// session has no flow in progress does not, and neither does a vacant seat.
func TestSeatRosterShowsTheFlowStep(t *testing.T) {
	s := newCeilingServer(t)
	grantAll(s)
	onFlow := seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": "flow"}, 200)
	idle := seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": "idle"}, 200)
	vacant := seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": "vacant"}, 200)
	if onFlow.Stage != nil || idle.Stage != nil || vacant.Stage != nil {
		t.Fatalf("vacant seats carry no stage: %+v %+v %+v", onFlow.Stage, idle.Stage, vacant.Stage)
	}
	registerSeatSession(t, s, "sess-flow", "p1", "plain")
	registerSeatSession(t, s, "sess-idle", "p1", "plain")
	seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats/"+onFlow.ID+"/bind", map[string]any{"ref": "sess-flow"}, 200)
	seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats/"+idle.ID+"/bind", map[string]any{"ref": "sess-idle"}, 200)

	// the session sitting in the first seat starts a flow; the other one starts none
	resp := stageDo(t, s, http.MethodPost, "/v1/work-items/stage", ceilTokPlain,
		map[string]any{"issue": "gofer-77", "template": "small", "project": "p1"}, map[string]string{sessionHeader: "sess-flow"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start flow = %d", resp.StatusCode)
	}
	var started stageViewBody
	decode(t, resp, &started)

	roster := seatRosterByName(t, s)
	got := roster["flow"].Stage
	if got == nil || got.WorkItemID != started.WorkItemID || got.Step != started.Step ||
		got.StepNo != 1 || got.StepTotal != len(started.Steps) || got.Status != "active" {
		t.Fatalf("the seat on a flow = %+v, want the flow %+v", got, started)
	}
	if v := roster["idle"].Stage; v != nil {
		t.Fatalf("a seat whose session has no flow must not carry stage: %+v", v)
	}
	if v := roster["vacant"].Stage; v != nil {
		t.Fatalf("a vacant seat must not carry stage: %+v", v)
	}

	// the single-seat answer carries the same object
	if v := seatJSON(t, s, ceilTokPlain, "", "GET", "/v1/seats/"+onFlow.ID, nil, 200); v.Stage == nil || v.Stage.Step != started.Step {
		t.Fatalf("GET one seat = %+v", v)
	}
	if v := seatJSON(t, s, ceilTokPlain, "", "GET", "/v1/seats/"+idle.ID, nil, 200); v.Stage != nil {
		t.Fatalf("GET the idle seat = %+v", v.Stage)
	}
}
