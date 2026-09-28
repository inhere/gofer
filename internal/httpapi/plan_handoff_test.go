package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func createHandoffTestPlan(t *testing.T, s *Server, token, id string) {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/plans", token, map[string]string{
		"plan_id": id,
		"title":   "handoff test",
	})
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("create plan status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPlanHandoffVersioning(t *testing.T) {
	s := newTestServer(t, testToken, false)
	const planID = "plan-handoff-version"
	createHandoffTestPlan(t, s, testToken, planID)

	put := func(body any) *http.Response {
		return do(t, s, http.MethodPut, "/v1/plans/"+planID+"/handoff", testToken, body)
	}
	resp := put(map[string]any{"body": "first", "expected_version": 0})
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("first handoff status=%d, want 200", resp.StatusCode)
	}
	var latest struct {
		Version int    `json:"version"`
		Body    string `json:"body"`
	}
	decode(t, resp, &latest)
	if latest.Version != 1 || latest.Body != "first" {
		t.Fatalf("first handoff=%+v, want version 1/body first", latest)
	}

	resp = put(map[string]any{"body": "second", "expected_version": 1})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second handoff status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &latest)
	if latest.Version != 2 || latest.Body != "second" {
		t.Fatalf("second handoff=%+v, want version 2/body second", latest)
	}

	resp = put(map[string]any{"body": "stale", "expected_version": 1})
	if resp.StatusCode != http.StatusConflict {
		defer resp.Body.Close()
		t.Fatalf("stale handoff status=%d, want 409", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, s, http.MethodGet, "/v1/plans/"+planID+"/handoff", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("latest handoff GET status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &latest)
	if latest.Version != 2 || latest.Body != "second" {
		t.Fatalf("latest handoff=%+v, want version 2/body second", latest)
	}

	resp = do(t, s, http.MethodGet, "/v1/plans/"+planID+"/handoff?version=1", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("version handoff GET status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &latest)
	if latest.Version != 1 || latest.Body != "first" {
		t.Fatalf("version 1 handoff=%+v, want first", latest)
	}

	resp = do(t, s, http.MethodGet, "/v1/plans/"+planID+"/handoff/history", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("handoff history status=%d, want 200", resp.StatusCode)
	}
	var history []struct {
		Version int `json:"version"`
	}
	decode(t, resp, &history)
	if len(history) != 2 || history[0].Version != 2 || history[1].Version != 1 {
		t.Fatalf("handoff history=%v, want [2,1]", history)
	}

	tooLarge := strings.Repeat("x", 16<<10+1)
	resp = put(map[string]any{"body": tooLarge, "expected_version": 2})
	if resp.StatusCode != http.StatusBadRequest {
		defer resp.Body.Close()
		t.Fatalf("oversize handoff status=%d, want 400", resp.StatusCode)
	}
}

func TestPlanHandoffPermissions(t *testing.T) {
	const userToken = "handoff-user-token"
	s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: userToken}}}, nil, nil)
	const planID = "plan-handoff-perms"
	createHandoffTestPlan(t, s, userToken, planID)

	// User caller can write.
	resp := do(t, s, http.MethodPut, "/v1/plans/"+planID+"/handoff", userToken,
		map[string]any{"body": "user", "expected_version": 0})
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("user handoff write status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	member := submitExecJob(t, s, userToken)
	attachedPlan := "plan-handoff-attached"
	createHandoffTestPlan(t, s, userToken, attachedPlan)
	if resp := do(t, s, http.MethodPost, "/v1/plans/"+attachedPlan+"/jobs", userToken, map[string]string{"job_id": member.ID}); resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("attach job status=%d, want 200", resp.StatusCode)
	}
	jobToken := seedJobToken(t, s, member.ID, jobstore.JobCredentialMember, attachedPlan)

	// Any job caller can read, including a job attached to another plan.
	resp = do(t, s, http.MethodGet, "/v1/plans/"+planID+"/handoff", jobToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("job handoff read status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// A job may write only the plan to which it is attached.
	resp = do(t, s, http.MethodPut, "/v1/plans/"+attachedPlan+"/handoff", jobToken,
		map[string]any{"body": "attached job", "expected_version": 0})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("attached job handoff write status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, s, http.MethodPut, "/v1/plans/"+planID+"/handoff", jobToken,
		map[string]any{"body": "wrong plan", "expected_version": 1})
	if resp.StatusCode != http.StatusForbidden {
		defer resp.Body.Close()
		t.Fatalf("other plan job handoff write status=%d, want 403", resp.StatusCode)
	}
}
