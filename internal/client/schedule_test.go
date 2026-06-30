package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

func TestScheduleClientMethods(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/schedules", func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		switch r.Method {
		case http.MethodPost:
			var req CreateScheduleRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			if req.Name != "nightly" || req.Cron != "*/5 * * * *" {
				t.Fatalf("create body schedule fields mismatch: %+v", req)
			}
			if req.Request.ProjectKey != "self" || req.Request.Agent != "exec" || len(req.Request.Cmd) != 2 {
				t.Fatalf("create body request mismatch: %+v", req.Request)
			}
			if req.CatchUp == nil || !*req.CatchUp {
				t.Fatalf("catch_up should be true in create body: %+v", req.CatchUp)
			}
			_ = json.NewEncoder(w).Encode(scheduleFixture("sch-1"))
		case http.MethodGet:
			if got := r.URL.Query().Get("project"); got != "self" {
				t.Fatalf("project query=%q want self", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schedules": []Schedule{scheduleFixture("sch-1")},
			})
		default:
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/v1/schedules/sch-1", func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(scheduleFixture("sch-1"))
		case http.MethodDelete:
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		default:
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/v1/schedules/sch-1/enable", func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		if r.Method != http.MethodPost {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		s := scheduleFixture("sch-1")
		s.Enabled = 1
		_ = json.NewEncoder(w).Encode(s)
	})
	mux.HandleFunc("/v1/schedules/sch-1/disable", func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		if r.Method != http.MethodPost {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		s := scheduleFixture("sch-1")
		s.Enabled = 0
		_ = json.NewEncoder(w).Encode(s)
	})
	mux.HandleFunc("/v1/schedules/sch-1/run-now", func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		if r.Method != http.MethodPost {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(w).Encode(job.JobResult{ID: "job-1", Status: job.StatusQueued})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cli := New(ts.URL, "tok")
	catchUp := true
	created, err := cli.CreateSchedule(CreateScheduleRequest{
		Name: "nightly",
		Cron: "*/5 * * * *",
		Request: job.JobRequest{
			ProjectKey: "self",
			Agent:      "exec",
			Runner:     "local",
			Cmd:        []string{"go", "version"},
		},
		CatchUp: &catchUp,
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if created.ID != "sch-1" || created.Request.ProjectKey != "self" {
		t.Fatalf("created schedule mismatch: %+v", created)
	}

	list, err := cli.ListSchedules("self")
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(list) != 1 || list[0].ID != "sch-1" {
		t.Fatalf("list mismatch: %+v", list)
	}

	got, err := cli.GetSchedule("sch-1")
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.ID != "sch-1" {
		t.Fatalf("get mismatch: %+v", got)
	}

	disabled, err := cli.SetScheduleEnabled("sch-1", false)
	if err != nil {
		t.Fatalf("SetScheduleEnabled(false): %v", err)
	}
	if disabled.Enabled != 0 {
		t.Fatalf("disable enabled=%d want 0", disabled.Enabled)
	}
	enabled, err := cli.SetScheduleEnabled("sch-1", true)
	if err != nil {
		t.Fatalf("SetScheduleEnabled(true): %v", err)
	}
	if enabled.Enabled != 1 {
		t.Fatalf("enable enabled=%d want 1", enabled.Enabled)
	}

	res, err := cli.RunSchedule("sch-1")
	if err != nil {
		t.Fatalf("RunSchedule: %v", err)
	}
	if res.ID != "job-1" || res.Status != job.StatusQueued {
		t.Fatalf("run result mismatch: %+v", res)
	}

	if err := cli.DeleteSchedule("sch-1"); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
}

func TestScheduleClientError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/schedules/missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unknown schedule", "detail": "missing"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	if _, err := New(ts.URL, "").GetSchedule("missing"); err == nil {
		t.Fatal("expected 404 error")
	} else if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "unknown schedule") {
		t.Fatalf("error should include server detail, got %v", err)
	}
}

func scheduleFixture(id string) Schedule {
	return Schedule{
		ID:         id,
		Name:       "nightly",
		Cron:       "*/5 * * * *",
		Enabled:    1,
		CatchUp:    1,
		NextRunAt:  1800000000,
		LastRunAt:  1700000000,
		LastJobID:  "job-prev",
		ProjectKey: "self",
		Request: job.JobRequest{
			ProjectKey: "self",
			Agent:      "exec",
			Runner:     "local",
			Cmd:        []string{"go", "version"},
		},
	}
}

func assertBearer(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer tok" {
		t.Fatalf("Authorization=%q want Bearer tok", got)
	}
}
