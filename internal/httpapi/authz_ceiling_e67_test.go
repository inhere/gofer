package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// 调用方上限第二期 E6（配置视图 / 定时事件）与 E7（上限拒绝审计）。

// TestConfigViewShowsCallerCeiling: the callers view of GET /v1/config carries each caller's
// ceiling, in exactly the shape whoami uses; a caller without one has no ceiling key at all.
func TestConfigViewShowsCallerCeiling(t *testing.T) {
	t.Parallel()
	readOnly := true
	s := recordingServerWith(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "mul", Token: "tok-mul", Ceiling: &config.CallerCeiling{Projects: []string{"p*"}, ReadOnly: &readOnly}},
			{ID: "plain", Token: ceilTokPlain},
		},
	}, nil)
	grantAll(s)

	status, body := ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/config", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/config = %d: %s", status, body)
	}
	view := decodeJSON(t, body)
	server, _ := view["server"].(map[string]any)
	rows, _ := server["callers"].([]any)
	if len(rows) != 2 {
		t.Fatalf("callers view = %v, want 2 rows", server["callers"])
	}
	byID := map[string]map[string]any{}
	for _, row := range rows {
		m, _ := row.(map[string]any)
		byID[m["id"].(string)] = m
	}
	ceil, _ := byID["mul"]["ceiling"].(map[string]any)
	if ceil == nil {
		t.Fatalf("caller mul = %v, want a ceiling block", byID["mul"])
	}
	if got, _ := json.Marshal(ceil["projects"]); string(got) != `["p*"]` {
		t.Errorf("mul ceiling projects = %s, want [\"p*\"]", got)
	}
	if ceil["read_only"] != true {
		t.Errorf("mul ceiling read_only = %v, want the effective true", ceil["read_only"])
	}
	if _, ok := byID["plain"]["ceiling"]; ok {
		t.Errorf("caller plain = %v, want no ceiling key", byID["plain"])
	}
}

// TestScheduleEventsRoute: GET /v1/schedules/{id}/events serves the schedule's own scope, is 404 for
// an unknown schedule, and is 404 for a schedule outside the caller's ceiling (Resource scope).
func TestScheduleEventsRoute(t *testing.T) {
	t.Parallel()
	s := recordingServerWith(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "lim", Token: "tok-lim", Ceiling: &config.CallerCeiling{Projects: []string{"p1"}}},
			{ID: "plain", Token: ceilTokPlain},
		},
	}, func(cfg *config.Config) {
		cfg.Projects["p1"] = config.ProjectConfig{HostPath: cfg.Storage.Root}
		cfg.Projects["other"] = config.ProjectConfig{HostPath: cfg.Storage.Root}
	})
	grantAll(s)
	now := time.Now().Unix()
	for _, rec := range []jobstore.ScheduleRecord{
		{ID: "sch-p1", Name: "a", ScheduleType: "cron", CronExpr: "*/5 * * * *", ProjectKey: "p1", Enabled: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "sch-other", Name: "b", ScheduleType: "cron", CronExpr: "*/5 * * * *", ProjectKey: "other", Enabled: 1, CreatedAt: now, UpdatedAt: now},
	} {
		if err := s.jobs.Meta().InsertSchedule(rec); err != nil {
			t.Fatalf("insert schedule %s: %v", rec.ID, err)
		}
	}
	s.jobs.RecordScopedEvent(job.ScheduleEventScope("sch-p1"), job.EventScheduleRunFailed, "p1",
		map[string]any{"schedule_id": "sch-p1", "reason": "refused by the ceiling"})

	status, body := ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/schedules/sch-p1/events", nil)
	if status != http.StatusOK {
		t.Fatalf("GET events = %d: %s", status, body)
	}
	events, _ := decodeJSON(t, body)["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("events = %v, want the one run_failed row", events)
	}
	if ev, _ := events[0].(map[string]any); ev["type"] != job.EventScheduleRunFailed {
		t.Errorf("event = %v, want type %s", events[0], job.EventScheduleRunFailed)
	}

	if status, body := ceilReq(t, s, "tok-lim", http.MethodGet, "/v1/schedules/sch-other/events", nil); status != http.StatusNotFound {
		t.Errorf("out-of-scope schedule events = %d: %s, want the 404 of an invisible resource", status, body)
	}
	if status, body := ceilReq(t, s, "tok-lim", http.MethodGet, "/v1/schedules/sch-p1/events", nil); status != http.StatusOK {
		t.Errorf("in-scope schedule events = %d: %s, want 200", status, body)
	}
	if status, body := ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/schedules/nope/events", nil); status != http.StatusNotFound {
		t.Errorf("unknown schedule events = %d: %s, want 404", status, body)
	}
}

// TestCeilingDenialIsAuditedOnce: a refused out-of-scope read leaves an auth_audit row naming the
// caller, the resource and the reason — and its own repetition within the window does not add a
// second row (the per-minute throttle). The unrestricted caller is untouched.
func TestCeilingDenialIsAuditedOnce(t *testing.T) {
	t.Parallel()
	resetCeilingAuditThrottle(t)
	s := recordingServerWith(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "audlim", Token: "tok-audlim", Ceiling: &config.CallerCeiling{Projects: []string{"p1"}}},
			{ID: "plain", Token: ceilTokPlain},
		},
	}, func(cfg *config.Config) {
		cfg.Projects["p1"] = config.ProjectConfig{HostPath: cfg.Storage.Root}
		cfg.Projects["other"] = config.ProjectConfig{HostPath: cfg.Storage.Root}
	})
	grantAll(s)
	now := time.Now().Unix()
	if err := s.jobs.Meta().InsertSchedule(jobstore.ScheduleRecord{
		ID: "sch-other", Name: "b", ScheduleType: "cron", CronExpr: "*/5 * * * *", ProjectKey: "other",
		Enabled: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	rows := func() []jobstore.AuthAuditEntry {
		t.Helper()
		list, err := s.jobs.Meta().ListAuthAudit(jobstore.AuthAuditFilter{Limit: 100})
		if err != nil {
			t.Fatalf("ListAuthAudit: %v", err)
		}
		return list
	}
	if got := rows(); len(got) != 0 {
		t.Fatalf("audit before any refusal = %v, want none", got)
	}

	for i := 0; i < 3; i++ {
		if status, body := ceilReq(t, s, "tok-audlim", http.MethodGet, "/v1/schedules/sch-other/events", nil); status != http.StatusNotFound {
			t.Fatalf("refused read #%d = %d: %s", i, status, body)
		}
	}
	audit := rows()
	if len(audit) != 1 {
		t.Fatalf("audit rows = %+v, want exactly one (throttled to one per caller/reason/kind per minute)", audit)
	}
	row := audit[0]
	if row.Actor != "audlim" || row.Action != "request.ceiling_denied" || row.Status != http.StatusNotFound {
		t.Errorf("audit row = %+v, want actor=audlim action=request.ceiling_denied status=404", row)
	}
	if row.Reason == "" || row.Subject != "caller:audlim" {
		t.Errorf("audit row = %+v, want the reason and the caller subject", row)
	}

	// The unrestricted caller is not audited: nothing about its requests is a refusal.
	if status, body := ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/schedules/sch-other/events", nil); status != http.StatusOK {
		t.Fatalf("unrestricted read = %d: %s", status, body)
	}
	if got := rows(); len(got) != 1 {
		t.Errorf("audit rows after the unrestricted read = %d, want still 1", len(got))
	}
}

// resetCeilingAuditThrottle clears the in-process ceiling-audit throttle, so this test neither
// inherits a window opened by another test nor leaves one behind (G053: global state is reset).
func resetCeilingAuditThrottle(t *testing.T) {
	t.Helper()
	ceilingAuditMu.Lock()
	ceilingAuditAt = map[ceilingAuditKey]time.Time{}
	ceilingAuditMu.Unlock()
	t.Cleanup(func() {
		ceilingAuditMu.Lock()
		ceilingAuditAt = map[ceilingAuditKey]time.Time{}
		ceilingAuditMu.Unlock()
	})
}
