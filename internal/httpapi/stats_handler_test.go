package httpapi

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/presence"
)

func TestStatsEndpointAggregatesJobsAndSchedules(t *testing.T) {
	const now = int64(1751500000000)
	orig := nowMillis
	nowMillis = func() int64 { return now }
	defer func() { nowMillis = orig }()

	s := newTestServer(t, testToken, false)
	s.SetBuildInfo(buildinfo.Info{Version: "v1.29", GitCommit: "abc123456789"})
	s.startedAt = time.UnixMilli(now - 125_000)
	meta := s.jobs.Meta()
	for _, rec := range []jobstore.JobRecord{
		statsJobRecord("job-1", job.StatusDone, 100),
		statsJobRecord("job-2", job.StatusDone, 200),
		statsJobRecord("job-3", job.StatusFailed, 300),
	} {
		if err := meta.UpsertJob(rec); err != nil {
			t.Fatalf("upsert job: %v", err)
		}
	}
	for _, rec := range []jobstore.ScheduleRecord{
		statsScheduleRecord("sch-1", 1),
		statsScheduleRecord("sch-2", 0),
	} {
		if err := meta.InsertSchedule(rec); err != nil {
			t.Fatalf("insert schedule: %v", err)
		}
	}
	presenceSvc := presence.NewService(meta)
	if _, err := presenceSvc.Register(presence.RegisterInput{Name: "sup", Role: "supervisor"}); err != nil {
		t.Fatalf("register supervisor: %v", err)
	}
	if _, err := presenceSvc.Register(presence.RegisterInput{Name: "worker", Role: "worker"}); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	s.SetPresence(presenceSvc)

	resp := do(t, s, http.MethodGet, "/v1/stats", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats status=%d, want 200", resp.StatusCode)
	}
	var body statsResp
	decode(t, resp, &body)

	if body.Jobs.Total != 3 {
		t.Fatalf("jobs.total=%d, want 3: %+v", body.Jobs.Total, body.Jobs.ByStatus)
	}
	if body.Jobs.ByStatus[job.StatusDone] != 2 || body.Jobs.ByStatus[job.StatusFailed] != 1 {
		t.Fatalf("jobs.by_status wrong: %+v", body.Jobs.ByStatus)
	}
	if body.Schedules.Total != 2 || body.Schedules.Enabled != 1 {
		t.Fatalf("schedules wrong: %+v", body.Schedules)
	}
	if body.Drivers.Online != 2 || body.Drivers.Supervisors != 1 {
		t.Fatalf("drivers wrong: %+v", body.Drivers)
	}
	if body.Workflows.Running != 0 || body.Workflows.Total != 0 {
		t.Fatalf("workflows should be zero placeholder: %+v", body.Workflows)
	}
	if body.Projects != 1 {
		t.Fatalf("projects=%d, want 1", body.Projects)
	}
	if body.ServerTime != now {
		t.Fatalf("server_time=%d, want %d", body.ServerTime, now)
	}
	if body.Version != "v1.29 (abc1234)" {
		t.Fatalf("version=%q, want v1.29 (abc1234)", body.Version)
	}
	if body.UptimeSec != 125 {
		t.Fatalf("uptime_sec=%d, want 125", body.UptimeSec)
	}
}

func statsJobRecord(id, status string, startedAt int64) jobstore.JobRecord {
	return jobstore.JobRecord{
		ID:         id,
		ProjectKey: "self",
		Agent:      "exec",
		Runner:     "local",
		Status:     status,
		Cwd:        ".",
		ResultDir:  ".",
		StartedAt:  startedAt,
		UpdatedAt:  startedAt,
	}
}

func statsScheduleRecord(id string, enabled int) jobstore.ScheduleRecord {
	return jobstore.ScheduleRecord{
		ID:          id,
		Name:        id,
		CronExpr:    "*/5 * * * *",
		RequestJSON: `{"project_key":"self","agent":"exec","runner":"local","cmd":["go","version"]}`,
		Enabled:     enabled,
		NextRunAt:   100,
		CatchUp:     1,
		ProjectKey:  "self",
		CreatedAt:   1,
		UpdatedAt:   1,
	}
}

// TestStatsIncludesDBAndSessions covers the U2 dashboard blocks: /v1/stats 必须
// 带上元数据库的文件/页/行数画像与 agent-session 聚合，且与预置数据一致。
func TestStatsIncludesDBAndSessions(t *testing.T) {
	s := newTestServer(t, testToken, false)
	meta := s.jobs.Meta()

	for _, rec := range []jobstore.JobRecord{
		statsJobRecord("job-db-1", job.StatusDone, 1000),
		statsJobRecord("job-db-2", job.StatusRunning, 1001),
		statsJobRecord("job-db-3", job.StatusQueued, 1002),
	} {
		if err := meta.UpsertJob(rec); err != nil {
			t.Fatalf("upsert job: %v", err)
		}
	}
	for _, in := range []jobstore.AgentSession{
		{SessionID: "sess-run", Agent: "claude", State: jobstore.SessionRunning, RelayMode: jobstore.RelayModeOn},
		{SessionID: "sess-wait", Agent: "codex", State: jobstore.SessionWaitingReply, RelayMode: jobstore.RelayModeAuto},
		{SessionID: "sess-end", Agent: "claude", State: jobstore.SessionEnded, RelayMode: jobstore.RelayModeOff},
	} {
		if _, err := meta.UpsertAgentSession(in); err != nil {
			t.Fatalf("upsert session: %v", err)
		}
	}
	// 一个仍在窗口内的 OPEN relay turn（计入等待），一个已答的（不计入）。
	for _, d := range []jobstore.PlanDecision{
		{ID: "dec-wait", Title: "t", Question: "q", TimeoutSec: 3600, SessionID: "sess-wait", Kind: jobstore.DecisionKindRelay},
		{ID: "dec-answered", Title: "t", Question: "q", TimeoutSec: 3600,
			State: jobstore.DecisionAnswered, SessionID: "sess-run", Kind: jobstore.DecisionKindRelay},
	} {
		dd := d
		if err := meta.InsertDecision(&dd); err != nil {
			t.Fatalf("insert decision %s: %v", d.ID, err)
		}
	}

	resp := do(t, s, http.MethodGet, "/v1/stats", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats status=%d, want 200", resp.StatusCode)
	}
	var body statsResp
	decode(t, resp, &body)

	if !filepath.IsAbs(body.DB.Path) || !strings.HasSuffix(body.DB.Path, "gofer.db") {
		t.Fatalf("db.path=%q, want an absolute .../gofer.db", body.DB.Path)
	}
	if body.DB.SizeBytes <= 0 {
		t.Fatalf("db.size_bytes=%d, want > 0", body.DB.SizeBytes)
	}
	if body.DB.PageSize <= 0 || body.DB.PageCount <= 0 {
		t.Fatalf("db page geometry wrong: size=%d count=%d", body.DB.PageSize, body.DB.PageCount)
	}
	if body.DB.Partial {
		t.Fatalf("db.partial=true with the default budget: %+v", body.DB)
	}
	for table, want := range map[string]int64{"jobs": 3, "agent_sessions": 3, "plan_decisions": 2} {
		got, ok := body.DB.Tables[table]
		if !ok {
			t.Fatalf("db.tables is missing %q: %+v", table, body.DB.Tables)
		}
		if got != want {
			t.Fatalf("db.tables[%s]=%d, want %d: %+v", table, got, want, body.DB.Tables)
		}
	}

	if body.Sessions.Total != 3 {
		t.Fatalf("sessions.total=%d, want 3: %+v", body.Sessions.Total, body.Sessions)
	}
	for state, want := range map[string]int{
		jobstore.SessionRunning: 1, jobstore.SessionWaitingReply: 1, jobstore.SessionEnded: 1,
		jobstore.SessionIdle: 0, jobstore.SessionNeedsAttention: 0,
	} {
		if got := body.Sessions.ByState[state]; got != want {
			t.Fatalf("sessions.by_state[%s]=%d, want %d: %+v", state, got, want, body.Sessions.ByState)
		}
	}
	for mode, want := range map[string]int{
		jobstore.RelayModeAuto: 1, jobstore.RelayModeOn: 1, jobstore.RelayModeOff: 1,
	} {
		if got := body.Sessions.ByRelayMode[mode]; got != want {
			t.Fatalf("sessions.by_relay_mode[%s]=%d, want %d: %+v", mode, got, want, body.Sessions.ByRelayMode)
		}
	}
	if body.Sessions.WaitingTurns != 1 {
		t.Fatalf("sessions.waiting_turns=%d, want 1: %+v", body.Sessions.WaitingTurns, body.Sessions)
	}
	if body.Sessions.SeenWithin1h != 3 {
		t.Fatalf("sessions.seen_within_1h=%d, want 3: %+v", body.Sessions.SeenWithin1h, body.Sessions)
	}
}

// TestStatsDBRowCountsPartialOnBudget：预算耗尽时行数降级为 partial（已得部分 +
// partial:true），文件/页信息与 HTTP 200 不受影响；预算充足时则完整且 partial=false。
func TestStatsDBRowCountsPartialOnBudget(t *testing.T) {
	s := newTestServer(t, testToken, false)
	if err := s.jobs.Meta().UpsertJob(statsJobRecord("job-budget-1", job.StatusDone, 1000)); err != nil {
		t.Fatalf("upsert job: %v", err)
	}

	orig := statsDBBudget
	statsDBBudget = 0
	defer func() { statsDBBudget = orig }()

	resp := do(t, s, http.MethodGet, "/v1/stats", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats status=%d, want 200 even on an exhausted budget", resp.StatusCode)
	}
	var body statsResp
	decode(t, resp, &body)

	if !body.DB.Partial {
		t.Fatalf("db.partial=false with a zero budget: %+v", body.DB)
	}
	if len(body.DB.Tables) != 0 {
		t.Fatalf("db.tables should be empty when the budget is exhausted upfront: %+v", body.DB.Tables)
	}
	if body.DB.SizeBytes <= 0 || body.DB.PageCount <= 0 {
		t.Fatalf("cheap db info must survive the degraded path: %+v", body.DB)
	}

	statsDBBudget = time.Minute
	resp = do(t, s, http.MethodGet, "/v1/stats", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &body)
	if body.DB.Partial {
		t.Fatalf("db.partial=true with a one-minute budget: %+v", body.DB)
	}
	if body.DB.Tables["jobs"] != 1 {
		t.Fatalf("db.tables[jobs]=%d, want 1: %+v", body.DB.Tables["jobs"], body.DB.Tables)
	}
}
