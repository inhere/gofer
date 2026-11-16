package commands

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestAuthAuditSendsFiltersAndPrintsLines: the filters become query parameters (a duration
// --since is turned into unix seconds), the lines print newest first, and the next-page
// cursor is shown.
func TestAuthAuditSendsFiltersAndPrintsLines(t *testing.T) {
	var got url.Values
	stubAuthServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audit" {
			http.NotFound(w, r)
			return
		}
		got = r.URL.Query()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"entries": []map[string]any{
				{"id": 9, "at": 1760000000, "caller_id": "alice", "method": "DELETE", "route": "DELETE /v1/schedules/{id}", "target": "id=s1", "status": 403, "error": "permission required: schedule.manage.others"},
				{"id": 8, "at": 1759999000, "method": "POST", "route": "POST /v1/jobs", "status": 201},
			},
			"count": 2, "next_cursor": 8,
		})
	}))
	c := bindCmd(newAuthAuditCmd())
	authAuditOpts.caller, authAuditOpts.status, authAuditOpts.since, authAuditOpts.limit = "alice", "fail", "24h", 2
	now := time.Unix(1760000000, 0)
	out := captureOutput(t, func() {
		if err := runAdminAudit(c, mustAuthClient(t), now); err != nil {
			t.Fatalf("auth audit: %v", err)
		}
	})
	if got.Get("caller") != "alice" || got.Get("status") != "fail" || got.Get("limit") != "2" || got.Get("since") != "1759913600" {
		t.Errorf("query = %v", got)
	}
	for _, want := range []string{"alice", "403", "DELETE /v1/schedules/{id}", "id=s1", "(permission required: schedule.manage.others)", "POST /v1/jobs", "more: --cursor 8"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestAuthAuditEmptyAndRefused: no lines says so; a server refusal is a coded failure.
func TestAuthAuditEmptyAndRefused(t *testing.T) {
	status := http.StatusOK
	stubAuthServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"entries": []any{}, "count": 0, "error": "permission required: audit.read"})
	}))
	c := bindCmd(newAuthAuditCmd())
	out := captureOutput(t, func() {
		if err := runAdminAudit(c, mustAuthClient(t), time.Now()); err != nil {
			t.Fatalf("empty audit: %v", err)
		}
	})
	if !strings.Contains(out, "no matching audit line") {
		t.Errorf("output = %q", out)
	}
	status = http.StatusForbidden
	if err := runAdminAudit(c, mustAuthClient(t), time.Now()); err == nil {
		t.Error("a 403 must fail the command")
	}
}
