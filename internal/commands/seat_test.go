package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seatStub is a minimal /v1/seats server: it answers the roster routes the seat commands call and
// records the bind request bodies, so the tests assert what the CLI actually put on the wire.
type seatStub struct {
	seats    []map[string]any
	binds    []map[string]any
	occupied bool
	sessions []map[string]any
	// openPlan is what GET /v1/seats/{id}/open-plan answers.
	openPlan map[string]any
	restart  map[string]any
}

func (s *seatStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{"projects": []any{
				map[string]any{"key": "p1", "host_path": "/work/p1", "container_path": "/work/p1"},
				map[string]any{"key": "p2", "host_path": "/work/p2"},
			}})
		case r.URL.Path == "/v1/sessions" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": s.sessions})
		case strings.HasPrefix(r.URL.Path, "/v1/sessions/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"session": map[string]any{"session_id": "sid-current", "agent": "claude", "state": "idle"}})
		case r.URL.Path == "/v1/seats" && r.Method == http.MethodGet:
			project := r.URL.Query().Get("project")
			rows := []map[string]any{}
			for _, x := range s.seats {
				if project == "" || x["project_key"] == project {
					rows = append(rows, x)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"seats": rows})
		case r.URL.Path == "/v1/seats" && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			body["id"], body["state"], body["charter_by"] = "seat-1", "none", "user"
			s.seats = append(s.seats, body)
			_ = json.NewEncoder(w).Encode(body)
		case strings.HasSuffix(r.URL.Path, "/bind"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.binds = append(s.binds, body)
			if s.occupied && body["replace"] != true {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "seat is occupied", "detail": "seat-1 is held by sid-old"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "seat-1", "name": "desk", "project_key": "p1",
				"kind": "terminal", "state": "running", "current_ref": body["ref"], "charter_by": "user"})
		case strings.HasSuffix(r.URL.Path, "/open-plan"):
			_ = json.NewEncoder(w).Encode(s.openPlan)
		case strings.HasSuffix(r.URL.Path, "/restart"):
			_ = json.NewEncoder(w).Encode(s.restart)
		case strings.HasPrefix(r.URL.Path, "/v1/seats/"):
			for _, x := range s.seats {
				if "/v1/seats/"+x["id"].(string) == r.URL.Path {
					_ = json.NewEncoder(w).Encode(x)
					return
				}
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

// seatTestStub starts the stub and points the CLI's connection flags at it.
func seatTestStub(t *testing.T, s *seatStub) {
	t.Helper()
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	jobConnOpts.server, jobConnOpts.token = srv.URL, "tok"
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
}

// TestSeatAddUsesTheCharterTemplate: `seat add --charter-template flow` sends the shipped
// template body, not the flag value.
func TestSeatAddUsesTheCharterTemplate(t *testing.T) {
	stub := &seatStub{}
	seatTestStub(t, stub)
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "add", "desk", "--purpose", "review PRs", "--charter-template", "flow", "-p", "p2"})
	})
	if code != 0 {
		t.Fatalf("add exit=%d out=%s", code, out)
	}
	if len(stub.seats) != 1 {
		t.Fatalf("seats = %+v", stub.seats)
	}
	charter, _ := stub.seats[0]["charter"].(string)
	for _, want := range []string{"## 1. Design before code", "gofer plan create", "## 5. Verify before you call it done"} {
		if !strings.Contains(charter, want) {
			t.Errorf("charter template missing %q:\n%s", want, charter)
		}
	}
	if stub.seats[0]["name"] != "desk" || stub.seats[0]["project_key"] != "p2" {
		t.Errorf("body = %+v", stub.seats[0])
	}
}

// TestSeatBindTakesTheCurrentSession: without --ref the session of the current directory is
// bound.
func TestSeatBindTakesTheCurrentSession(t *testing.T) {
	stub := &seatStub{
		seats:    []map[string]any{{"id": "seat-1", "name": "desk", "project_key": "p1", "kind": "terminal", "state": "none", "charter_by": "user"}},
		sessions: []map[string]any{{"session_id": "sid-current", "agent": "claude", "state": "idle", "cwd": ""}},
	}
	seatTestStub(t, stub)
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "bind", "desk@p1"})
	})
	if code != 0 {
		t.Fatalf("bind exit=%d out=%s", code, out)
	}
	if len(stub.binds) != 1 || stub.binds[0]["ref"] != "sid-current" {
		t.Fatalf("binds = %+v", stub.binds)
	}
	if !strings.Contains(out, "relay hook") {
		t.Errorf("bind must say what charter injection needs: %s", out)
	}
}

// TestSeatBindOccupiedSuggestsReplace: a 409 from the server becomes the actionable hint.
func TestSeatBindOccupiedSuggestsReplace(t *testing.T) {
	stub := &seatStub{
		occupied: true,
		seats:    []map[string]any{{"id": "seat-1", "name": "desk", "project_key": "p1", "kind": "terminal", "state": "running", "charter_by": "user"}},
		sessions: []map[string]any{{"session_id": "sid-current", "agent": "claude", "state": "idle"}},
	}
	seatTestStub(t, stub)
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "bind", "desk", "--ref", "sid-current"})
	})
	if code == 0 {
		t.Fatalf("an occupied seat must not bind silently: %s", out)
	}
	if !strings.Contains(out, "--replace") {
		t.Errorf("the refusal must point at --replace: %s", out)
	}
}

// TestSeatOpenPrintsBothShellsAndTheMachine: sh / ps spellings and the runner are printed.
func TestSeatOpenPrintsBothShellsAndTheMachine(t *testing.T) {
	plan := map[string]any{"seat_id": "seat-1", "kind": "terminal", "runner": "w1", "cwd": "/work/p1",
		"resumable": true, "sh": "cd /work/p1 && claude --resume sid-1", "powershell": "Set-Location /work/p1; claude --resume sid-1"}
	for _, shell := range []string{"sh", "ps"} {
		stub := &seatStub{
			seats:    []map[string]any{{"id": "seat-1", "name": "desk", "project_key": "p1", "kind": "terminal", "state": "ended", "charter_by": "user"}},
			openPlan: plan,
		}
		seatTestStub(t, stub)
		var code int
		out := captureOutput(t, func() {
			code = NewApp("test").Run([]string{"seat", "open", "desk", "--shell", shell})
		})
		if code != 0 {
			t.Fatalf("open --shell %s exit=%d out=%s", shell, code, out)
		}
		if !strings.Contains(out, "w1") || !strings.Contains(out, "/work/p1") {
			t.Errorf("open --shell %s must say where to run it: %s", shell, out)
		}
		want := "cd /work/p1 && claude --resume sid-1"
		if shell == "ps" {
			want = "Set-Location /work/p1; claude --resume sid-1"
		}
		if !strings.Contains(out, want) {
			t.Errorf("open --shell %s printed %q, want %q", shell, out, want)
		}
	}
}

// TestSeatOpenACPSaysRestart: an ended ACP session is not resumed by hand; the command points at
// --restart and at the workbench thread.
func TestSeatOpenACPSaysRestart(t *testing.T) {
	stub := &seatStub{
		seats:    []map[string]any{{"id": "seat-1", "name": "review", "project_key": "p1", "kind": "acp", "state": "ended", "charter_by": "user"}},
		openPlan: map[string]any{"seat_id": "seat-1", "kind": "acp", "thread_id": "r:sid-9", "needs_restart": true, "reason": "needs_restart"},
		restart:  map[string]any{"seat_id": "seat-1", "kind": "acp", "thread_id": "job:job-2", "restarted": true, "job_id": "job-2"},
	}
	seatTestStub(t, stub)
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "open", "review"})
	})
	if code != 0 {
		t.Fatalf("open exit=%d out=%s", code, out)
	}
	if !strings.Contains(out, "r:sid-9") || !strings.Contains(out, "--restart") {
		t.Errorf("acp open must show the thread and the restart hint: %s", out)
	}
	out = captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "open", "review", "--restart"})
	})
	if code != 0 {
		t.Fatalf("open --restart exit=%d out=%s", code, out)
	}
	if !strings.Contains(out, "job-2") || !strings.Contains(out, "job:job-2") {
		t.Errorf("restart must print the new job and thread: %s", out)
	}
}

// TestSeatNamesAreResolvedPerProject: name@project picks the named project, and a bare name that
// matches seats in several projects is refused with both candidates.
func TestSeatNamesAreResolvedPerProject(t *testing.T) {
	stub := &seatStub{seats: []map[string]any{
		{"id": "seat-1", "name": "desk", "project_key": "p1", "kind": "terminal", "state": "none", "charter_by": "user"},
		{"id": "seat-2", "name": "desk", "project_key": "p2", "kind": "terminal", "state": "none", "charter_by": "user"},
	}}
	seatTestStub(t, stub)
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "show", "desk@p2"})
	})
	if code != 0 || !strings.Contains(out, "p2") {
		t.Fatalf("show desk@p2 exit=%d out=%s", code, out)
	}
	// The cwd sits in no project: both seats match, so the name is refused.
	out = captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "show", "desk"})
	})
	if code == 0 || !strings.Contains(out, "desk@p1") || !strings.Contains(out, "desk@p2") {
		t.Fatalf("an ambiguous name must list both: exit=%d out=%s", code, out)
	}
}
