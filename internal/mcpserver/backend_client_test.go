package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// mockBackend starts an httptest server fronting mux and returns a clientBackend
// pointed at it (auto-cleaned). The bearer token is non-empty so the client
// attaches Authorization; the mock ignores it.
func mockBackend(t *testing.T, mux *http.ServeMux) Backend {
	t.Helper()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return NewClientBackend(client.New(ts.URL, "tok"))
}

// TestClientBackendRunJob: RunJob POSTs /v1/jobs and returns the initial async
// snapshot (queued) the server echoes — matching localBackend.RunJob semantics.
func TestClientBackendRunJob(t *testing.T) {
	mux := http.NewServeMux()
	var gotMethod string
	mux.HandleFunc("/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		_ = json.NewEncoder(w).Encode(job.JobResult{ID: "j1", Status: "queued", ProjectKey: "self"})
	})
	b := mockBackend(t, mux)

	res, err := b.RunJob(job.JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local"})
	if err != nil {
		t.Fatalf("RunJob: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("RunJob method=%s want POST", gotMethod)
	}
	if res.ID != "j1" || res.Status != "queued" {
		t.Fatalf("RunJob result mismatch: %+v", res)
	}
}

// TestClientBackendGetJobAndResult: GetJob fetches the snapshot; GetResult
// returns the snapshot's result_json (both hit GET /v1/jobs/{id}).
func TestClientBackendGetJobAndResult(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/jobs/j1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(w).Encode(job.JobResult{ID: "j1", Status: "done", ResultJSON: `{"ok":true}`})
	})
	b := mockBackend(t, mux)

	res, err := b.GetJob("j1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if res.ID != "j1" || res.Status != "done" {
		t.Fatalf("GetJob mismatch: %+v", res)
	}

	rj, err := b.GetResult("j1")
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if rj != `{"ok":true}` {
		t.Fatalf("GetResult=%q want result_json", rj)
	}
}

// TestClientBackendCancelJob: CancelJob POSTs /v1/jobs/{id}/cancel and returns
// the resulting snapshot.
func TestClientBackendCancelJob(t *testing.T) {
	mux := http.NewServeMux()
	var gotMethod string
	mux.HandleFunc("/v1/jobs/j1/cancel", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		_ = json.NewEncoder(w).Encode(job.JobResult{ID: "j1", Status: "cancelled"})
	})
	b := mockBackend(t, mux)

	res, err := b.CancelJob("j1")
	if err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("CancelJob method=%s want POST", gotMethod)
	}
	if res.Status != "cancelled" {
		t.Fatalf("CancelJob status=%s want cancelled", res.Status)
	}
}

// TestClientBackendGetInteractions: forwards to GET /v1/jobs/{id}/interactions
// and unwraps the envelope into []job.Interaction.
func TestClientBackendGetInteractions(t *testing.T) {
	want := []job.Interaction{
		{ID: "int-1", JobID: "j1", Type: "question", Prompt: "ok?", Status: "pending", CreatedAt: 100},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/jobs/j1/interactions", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"interactions": want})
	})
	b := mockBackend(t, mux)

	got, err := b.GetInteractions("j1")
	if err != nil {
		t.Fatalf("GetInteractions: %v", err)
	}
	if len(got) != 1 || got[0].ID != "int-1" || got[0].Status != "pending" {
		t.Fatalf("GetInteractions mismatch: %+v", got)
	}
}

// TestClientBackendAnswerInteraction: POSTs the answer and decodes the updated
// Interaction the endpoint echoes back.
func TestClientBackendAnswerInteraction(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/jobs/j1/interactions/int-1/answer", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Answer string `json:"answer"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(job.Interaction{
			ID: "int-1", JobID: "j1", Status: "answered", Answer: body.Answer, AnsweredAt: 200,
		})
	})
	b := mockBackend(t, mux)

	it, err := b.AnswerInteraction("j1", "int-1", "yes", "")
	if err != nil {
		t.Fatalf("AnswerInteraction: %v", err)
	}
	if it.Status != "answered" || it.Answer != "yes" || it.AnsweredAt != 200 {
		t.Fatalf("AnswerInteraction mismatch: %+v", it)
	}
}

// TestClientBackendTailLogTrim: the server returns the whole tail; the backend
// trims to the last maxBytes bytes client-side (maxBytes<=0 means no cap).
func TestClientBackendTailLogTrim(t *testing.T) {
	const full = "0123456789ABCDEF" // 16 bytes
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/jobs/j1/logs/stdout", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(full))
	})
	b := mockBackend(t, mux)

	// maxBytes < len → only the last N bytes.
	got, err := b.TailLog("j1", "stdout", 4)
	if err != nil {
		t.Fatalf("TailLog: %v", err)
	}
	if got != "CDEF" {
		t.Fatalf("TailLog trim=%q want last 4 bytes %q", got, "CDEF")
	}

	// maxBytes==0 → no cap, full body.
	got, err = b.TailLog("j1", "stdout", 0)
	if err != nil {
		t.Fatalf("TailLog full: %v", err)
	}
	if got != full {
		t.Fatalf("TailLog full=%q want %q", got, full)
	}

	// maxBytes >= len → unchanged.
	got, err = b.TailLog("j1", "stdout", 100)
	if err != nil {
		t.Fatalf("TailLog big cap: %v", err)
	}
	if got != full {
		t.Fatalf("TailLog big cap=%q want %q", got, full)
	}
}

// TestClientBackendListProjects: maps /v1/meta projects into projectEntry with
// host_path/container_path left empty (server paths not exposed) and the
// allowlists/default carried through. The slice is non-nil even when empty.
func TestClientBackendListProjects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/meta", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"projects": []map[string]any{
				{"key": "self", "allowed_agents": []string{"exec"}, "allowed_runners": []string{"local"}, "default_agent": "exec"},
			},
		})
	})
	b := mockBackend(t, mux)

	got, err := b.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if got == nil {
		t.Fatal("ListProjects returned nil slice, want non-nil")
	}
	if len(got) != 1 {
		t.Fatalf("got %d projects want 1: %+v", len(got), got)
	}
	p := got[0]
	if p.Key != "self" || p.DefaultAgent != "exec" {
		t.Fatalf("project key/default mismatch: %+v", p)
	}
	if p.HostPath != "" || p.ContainerPath != "" {
		t.Fatalf("host_path/container_path must be empty (server paths not exposed): %+v", p)
	}
	if len(p.AllowedAgents) != 1 || p.AllowedAgents[0] != "exec" {
		t.Fatalf("allowed_agents lost: %+v", p)
	}
	if len(p.AllowedRunners) != 1 || p.AllowedRunners[0] != "local" {
		t.Fatalf("allowed_runners lost: %+v", p)
	}
}

// TestClientBackendListProjectsEmptyNonNil: an empty server listing still yields
// a non-nil slice (matching localBackend).
func TestClientBackendListProjectsEmptyNonNil(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/meta", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"projects": []any{}})
	})
	b := mockBackend(t, mux)

	got, err := b.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if got == nil {
		t.Fatal("empty ListProjects returned nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("want 0 projects, got %d", len(got))
	}
}

// TestClientBackendListAgents: client.ListAgents folds the /v1/agents wire shape
// (key/type/available/version/error) into name/type/available/detail; the
// backend maps that 1:1 into agentEntry.
func TestClientBackendListAgents(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"agents": []map[string]any{
				{"key": "claude", "type": "cli", "available": true, "version": "1.2.3"},
				{"key": "exec", "type": "exec", "available": false, "error": "not found"},
			},
		})
	})
	b := mockBackend(t, mux)

	got, err := b.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d agents want 2: %+v", len(got), got)
	}
	if got[0].Name != "claude" || got[0].Type != "cli" || !got[0].Available || got[0].Detail != "1.2.3" {
		t.Fatalf("agent[0] mismatch: %+v", got[0])
	}
	if got[1].Name != "exec" || got[1].Available || got[1].Detail != "not found" {
		t.Fatalf("agent[1] mismatch: %+v", got[1])
	}
}

// TestClientBackendGetArtifacts: client.ListArtifacts returns the inner
// `[{name,size,mtime},...]` array as raw JSON; the backend parses it into
// []artifactView.
func TestClientBackendGetArtifacts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/jobs/j1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"artifacts": []map[string]any{
				{"name": "out.txt", "size": 12, "mtime": 1700000000},
				{"name": "sub/b.bin", "size": 34, "mtime": 1700000001},
			},
		})
	})
	b := mockBackend(t, mux)

	got, err := b.GetArtifacts("j1")
	if err != nil {
		t.Fatalf("GetArtifacts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d artifacts want 2: %+v", len(got), got)
	}
	if got[0].Name != "out.txt" || got[0].Size != 12 || got[0].Mtime != 1700000000 {
		t.Fatalf("artifact[0] mismatch: %+v", got[0])
	}
	if got[1].Name != "sub/b.bin" || got[1].Size != 34 {
		t.Fatalf("artifact[1] mismatch: %+v", got[1])
	}
}

// TestClientBackendGetArtifactsEmptyNonNil: an empty manifest ({"artifacts":[]})
// yields a non-nil empty slice (matching localBackend).
func TestClientBackendGetArtifactsEmptyNonNil(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/jobs/j1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"artifacts":[]}`))
	})
	b := mockBackend(t, mux)

	got, err := b.GetArtifacts("j1")
	if err != nil {
		t.Fatalf("GetArtifacts: %v", err)
	}
	if got == nil {
		t.Fatal("empty GetArtifacts returned nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("want 0 artifacts, got %d: %+v", len(got), got)
	}
}

func TestClientBackendPlanMethods(t *testing.T) {
	mux := http.NewServeMux()
	var sawCreate, sawAttach, sawAddTodo, sawUpdateTodo bool
	mux.HandleFunc("/v1/plans", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		sawCreate = true
		var body struct {
			PlanID      string `json:"plan_id"`
			Title       string `json:"title"`
			Description string `json:"description"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.PlanID != "" || body.Title != "plan title" || body.Description != "desc" {
			t.Fatalf("create body mismatch: %+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_id":     "plan-1",
			"title":       body.Title,
			"description": body.Description,
			"status":      "open",
			"created_at":  int64(100),
			"updated_at":  int64(100),
		})
	})
	mux.HandleFunc("/v1/plans/plan-1/jobs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		sawAttach = true
		var body struct {
			JobID string `json:"job_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.JobID != "job-1" {
			t.Fatalf("attach body mismatch: %+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_id": "plan-1",
			"title":   "plan title",
			"status":  "open",
		})
	})
	mux.HandleFunc("/v1/plans/plan-1/todos", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		sawAddTodo = true
		var body struct {
			Title string `json:"title"`
			JobID string `json:"job_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Title != "todo title" || body.JobID != "job-1" {
			t.Fatalf("add todo body mismatch: %+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"todo_id":    "todo-1",
			"plan_id":    "plan-1",
			"job_id":     body.JobID,
			"title":      body.Title,
			"done":       false,
			"created_at": int64(300),
			"updated_at": int64(300),
		})
	})
	mux.HandleFunc("/v1/todos/todo-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		sawUpdateTodo = true
		var body struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Status != "done" {
			t.Fatalf("update todo status mismatch: %+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"todo_id":    "todo-1",
			"plan_id":    "plan-1",
			"job_id":     "job-1",
			"title":      "todo title",
			"done":       true,
			"status":     "done",
			"created_at": int64(300),
			"updated_at": int64(400),
		})
	})
	mux.HandleFunc("/v1/plans/plan-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_id":    "plan-1",
			"title":      "plan title",
			"status":     "open",
			"created_at": int64(100),
			"updated_at": int64(200),
			"counts": map[string]any{
				"total": 2, "queued": 0, "running": 1, "done": 1, "failed": 0,
			},
			"jobs": []map[string]any{
				{"id": "job-1", "status": "done", "project_key": "self", "agent": "exec", "runner": "local", "plan_id": "plan-1"},
				{"id": "job-2", "status": "running", "project_key": "self", "agent": "exec", "runner": "local", "plan_id": "plan-1"},
			},
			"todos": []map[string]any{
				{"todo_id": "todo-1", "plan_id": "plan-1", "job_id": "job-1", "title": "todo title", "done": true, "created_at": int64(300), "updated_at": int64(400)},
			},
		})
	})
	b := mockBackend(t, mux)

	created, err := b.CreatePlan("plan title", "desc")
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if !sawCreate || created.PlanID != "plan-1" || created.Jobs == nil || len(created.Jobs) != 0 {
		t.Fatalf("created plan mismatch: saw=%v plan=%+v", sawCreate, created)
	}

	attached, err := b.AttachJob("plan-1", "job-1")
	if err != nil {
		t.Fatalf("AttachJob: %v", err)
	}
	if !sawAttach || attached.PlanID != "plan-1" || attached.Jobs == nil || len(attached.Jobs) != 0 {
		t.Fatalf("attached plan mismatch: saw=%v plan=%+v", sawAttach, attached)
	}

	got, err := b.GetPlan("plan-1")
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Counts != (jobstore.PlanCounts{Total: 2, Running: 1, Done: 1}) {
		t.Fatalf("GetPlan counts mismatch: %+v", got.Counts)
	}
	if len(got.Jobs) != 2 || got.Jobs[0].PlanID != "plan-1" || got.Jobs[1].Status != "running" {
		t.Fatalf("GetPlan jobs mismatch: %+v", got.Jobs)
	}
	if len(got.Todos) != 1 || got.Todos[0].TodoID != "todo-1" || !got.Todos[0].Done || got.Todos[0].JobID != "job-1" {
		t.Fatalf("GetPlan todos mismatch: %+v", got.Todos)
	}

	added, err := b.AddTodo("plan-1", "todo title", "job-1", "")
	if err != nil {
		t.Fatalf("AddTodo: %v", err)
	}
	if !sawAddTodo || added.TodoID != "todo-1" || added.JobID != "job-1" || added.Done {
		t.Fatalf("AddTodo mismatch: saw=%v todo=%+v", sawAddTodo, added)
	}
	updated, err := b.UpdateTodo("todo-1", "done", nil)
	if err != nil {
		t.Fatalf("UpdateTodo: %v", err)
	}
	if !sawUpdateTodo || updated.TodoID != "todo-1" || !updated.Done {
		t.Fatalf("UpdateTodo mismatch: saw=%v todo=%+v", sawUpdateTodo, updated)
	}
}

func TestClientPlanToViewNilCountsAndJobs(t *testing.T) {
	pv := clientPlanToView(client.Plan{PlanID: "plan-nil", Status: "open"})
	if pv.Counts != (jobstore.PlanCounts{}) {
		t.Fatalf("nil counts should map to zero value, got %+v", pv.Counts)
	}
	if pv.Jobs == nil || len(pv.Jobs) != 0 {
		t.Fatalf("nil jobs should map to non-nil empty slice, got %+v", pv.Jobs)
	}
	if pv.Todos == nil || len(pv.Todos) != 0 {
		t.Fatalf("nil todos should map to non-nil empty slice, got %+v", pv.Todos)
	}
}

// TestClientBackendGetJobErrorPropagates: a 404 from the server surfaces as an
// error (sanity that forwarding propagates failures, not just happy paths).
func TestClientBackendGetJobErrorPropagates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/jobs/nope", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unknown job"})
	})
	b := mockBackend(t, mux)

	if _, err := b.GetJob("nope"); err == nil {
		t.Fatal("expected error for unknown job")
	} else if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention 404: %v", err)
	}
}
