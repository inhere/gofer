package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/driver"
)

// createDriverPlan creates a plan owned by the test caller and returns its id.
func createDriverPlan(t *testing.T, s *Server, id string) string {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/plans", testToken, map[string]any{
		"plan_id": id, "title": "driven", "project": "self",
	})
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("create plan status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	return id
}

// TestPlanDriverRequiresAuth: none of the three entries is reachable without a token.
func TestPlanDriverRequiresAuth(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	id := createDriverPlan(t, s, "plan-driver-auth")

	for _, tc := range []struct {
		method string
		body   any
	}{
		{http.MethodPut, map[string]any{"level": "record"}},
		{http.MethodGet, nil},
		{http.MethodDelete, nil},
	} {
		resp := do(t, s, tc.method, "/v1/plans/"+id+"/driver", "", tc.body)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated status=%d, want 401", tc.method, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// TestPlanDriverEnableReadDisable: PUT stores the level and the budget, GET reads the same
// state back, and DELETE turns it off — idempotently.
func TestPlanDriverEnableReadDisable(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	id := createDriverPlan(t, s, "plan-driver-http")

	// Before enabling: off, with an all-unlimited budget.
	resp := do(t, s, http.MethodGet, "/v1/plans/"+id+"/driver", testToken, nil)
	var st job.PlanDriverStatus
	decode(t, resp, &st)
	if st.Enabled || st.Level != "" || st.Budget.MaxJobs != 0 {
		t.Fatalf("a fresh plan reports %+v, want the driver off", st)
	}

	resp = do(t, s, http.MethodPut, "/v1/plans/"+id+"/driver", testToken, map[string]any{
		"level":  "record",
		"budget": map[string]any{"max_jobs": 7, "max_rework": 2, "max_hours": 1.5},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enable status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &st)
	if !st.Enabled || st.Level != job.PlanDriverLevelRecord {
		t.Fatalf("enable answered %+v, want record enabled", st)
	}
	if st.Budget.MaxJobs != 7 || st.Budget.MaxRework != 2 || st.Budget.MaxHours != 1.5 {
		t.Fatalf("budget = %+v, want the stored one", st.Budget)
	}
	if st.EnabledAt == 0 {
		t.Fatalf("enable did not stamp the accounting window: %+v", st)
	}

	// The read answers the same state (the budget survives the JSON round trip).
	resp = do(t, s, http.MethodGet, "/v1/plans/"+id+"/driver", testToken, nil)
	var read job.PlanDriverStatus
	decode(t, resp, &read)
	if !read.Enabled || read.Level != job.PlanDriverLevelRecord || read.Budget.MaxJobs != 7 {
		t.Fatalf("read answered %+v, want the enabled driver", read)
	}

	resp = do(t, s, http.MethodDelete, "/v1/plans/"+id+"/driver", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disable status=%d, want 200", resp.StatusCode)
	}
	var off job.PlanDriverStatus
	decode(t, resp, &off)
	if off.Enabled || off.Level != "" {
		t.Fatalf("disable answered %+v, want the driver off", off)
	}
	// Idempotent: asking again answers the same off state.
	resp = do(t, s, http.MethodDelete, "/v1/plans/"+id+"/driver", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second disable status=%d, want 200", resp.StatusCode)
	}
	var offAgain job.PlanDriverStatus
	decode(t, resp, &offAgain)
	if offAgain.Enabled {
		t.Fatalf("second disable answered %+v, want the driver off", offAgain)
	}
}

// TestPlanDriverRefusesLaterLevelsAndBadBudgets: `assist` / `auto` are a later version's
// levels and a negative budget is a typo, so all of them are 400s that write nothing.
func TestPlanDriverRefusesLaterLevelsAndBadBudgets(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	id := createDriverPlan(t, s, "plan-driver-refuse")

	for _, body := range []map[string]any{
		{"level": "assist"},
		{"level": "auto"},
		{"level": "turbo"},
		{"level": "record", "budget": map[string]any{"max_jobs": -1}},
		{"level": "record", "budget": map[string]any{"max_hours": -0.5}},
	} {
		resp := do(t, s, http.MethodPut, "/v1/plans/"+id+"/driver", testToken, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %v status=%d, want 400", body, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// Nothing was stored by any of the refused requests.
	resp := do(t, s, http.MethodGet, "/v1/plans/"+id+"/driver", testToken, nil)
	var st job.PlanDriverStatus
	decode(t, resp, &st)
	if st.Enabled {
		t.Fatalf("a refused request enabled the driver: %+v", st)
	}
}

// TestPlanDriverUnknownPlan: the three entries answer 404 for a plan no gofer has.
func TestPlanDriverUnknownPlan(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)

	resp := do(t, s, http.MethodGet, "/v1/plans/plan-no-such/driver", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodPut, "/v1/plans/plan-no-such/driver", testToken, map[string]any{"level": "record"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("PUT status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodDelete, "/v1/plans/plan-no-such/driver", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestPlanDriverInvalidBody: a body that is not JSON is the caller's error, and the
// refusal names it instead of driving the plan.
func TestPlanDriverInvalidBody(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	id := createDriverPlan(t, s, "plan-driver-badbody")

	resp := do(t, s, http.MethodPut, "/v1/plans/"+id+"/driver", testToken, "not-an-object")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", resp.StatusCode)
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if !strings.Contains(strings.ToLower(e.Error), "body") {
		t.Fatalf("error = %q, want it to name the body", e.Error)
	}
}

// TestPlanDriverReportRoute: the report needs a token, is a 404 for an unknown plan, and
// answers the (empty) report of a plan whose driver just turned on.
func TestPlanDriverReportRoute(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	s.jobs.SetPlanDriver(driver.NewEngine(s.jobs))
	id := createDriverPlan(t, s, "plan-driver-report")

	resp := do(t, s, http.MethodGet, "/v1/plans/"+id+"/driver/report", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated report status=%d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, s, http.MethodGet, "/v1/plans/no-such-plan/driver/report", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown plan report status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, s, http.MethodPut, "/v1/plans/"+id+"/driver", testToken, map[string]any{"budget": map[string]any{"max_jobs": 4}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enable status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/plans/"+id+"/driver/report", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("report status=%d, want 200", resp.StatusCode)
	}
	var rep job.PlanDriverReport
	decode(t, resp, &rep)
	if !rep.Enabled || rep.Level != job.PlanDriverLevelRecord || rep.Budget.MaxJobs != 4 || rep.Total != 0 || rep.WindowHours != 24 {
		t.Fatalf("report = %+v, want an enabled record driver with budget 4 and nothing recorded", rep)
	}
	if rep.ByAction == nil || rep.ByRule == nil {
		t.Fatalf("report lists must encode as [], got %+v", rep)
	}
}
