package httpapi

import (
	"net/http"
	"testing"

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
