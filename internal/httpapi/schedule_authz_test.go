package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// scheduleAuthzServer builds a server with an owner (alice), an unrelated caller (bob)
// and a root administrator (root).
func scheduleAuthzServer(t *testing.T) *Server {
	t.Helper()
	return newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
			{ID: "root", Token: "tok-root", CanAdmin: true},
		},
	})
}

// createWebhookSchedule creates alice's webhook schedule and returns the view the
// create answered with (the owner sees the real secret).
func createWebhookSchedule(t *testing.T, s *Server, token string) scheduleView {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/schedules", token, createScheduleReq{
		Name:    "nightly",
		Cron:    "*/5 * * * *",
		Webhook: true,
		Request: job.JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create schedule status=%d, want 200", resp.StatusCode)
	}
	var view scheduleView
	decode(t, resp, &view)
	if view.TriggerToken == "" {
		t.Fatal("a --webhook create must answer with the minted trigger token")
	}
	return view
}

// TestScheduleTriggerTokenMaskedForNonOwner (gofer-hxld): the schedule's webhook secret
// belongs to the caller that created it and to a root administrator. Everyone else gets the first
// four characters and stars, on the list and on the single read, so an unrelated caller — one
// holding `schedule.manage.others` included: the token is a credential for an endpoint that takes
// no permission, not a capability of the schedule's manager — cannot walk away with it.
func TestScheduleTriggerTokenMaskedForNonOwner(t *testing.T) {
	t.Parallel()
	s := scheduleAuthzServer(t)
	created := createWebhookSchedule(t, s, "tok-alice")
	secret := created.TriggerToken

	masked := secret[:4] + strings.Repeat("*", len(secret)-4)
	cases := []struct {
		name  string
		token string
		want  string
	}{
		{name: "owner", token: "tok-alice", want: secret},
		{name: "root administrator", token: "tok-root", want: secret},
		// bob holds the seeded schedule.manage.others — what a default deployment gives every
		// user-class caller — and is masked all the same.
		{name: "another caller holding schedule.manage.others", token: "tok-bob", want: masked},
	}
	for _, tc := range cases {
		resp := do(t, s, http.MethodGet, "/v1/schedules/"+created.ID, tc.token, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s get status=%d, want 200", tc.name, resp.StatusCode)
		}
		var view scheduleView
		decode(t, resp, &view)
		if view.TriggerToken != tc.want {
			t.Fatalf("%s get trigger_token=%q, want %q", tc.name, view.TriggerToken, tc.want)
		}

		resp = do(t, s, http.MethodGet, "/v1/schedules", tc.token, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s list status=%d, want 200", tc.name, resp.StatusCode)
		}
		var list struct {
			Schedules []scheduleView `json:"schedules"`
		}
		decode(t, resp, &list)
		if len(list.Schedules) != 1 {
			t.Fatalf("%s list = %+v, want the one schedule", tc.name, list.Schedules)
		}
		if list.Schedules[0].TriggerToken != tc.want {
			t.Fatalf("%s list trigger_token=%q, want %q", tc.name, list.Schedules[0].TriggerToken, tc.want)
		}
	}

	// A schedule with no recorded creator (a row whose stored request carries no caller
	// id) has no owner to match, so its secret is masked for every caller but a root
	// administrator — including one that owns a DIFFERENT schedule.
	raw, err := json.Marshal(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	const ownerlessToken = "zzzzSECRETTOKEN"
	ownerlessWant := "zzzz" + strings.Repeat("*", len(ownerlessToken)-4)
	if err := s.jobs.Meta().InsertSchedule(jobstore.ScheduleRecord{
		ID: "sch-ownerless", Name: "ownerless", ScheduleType: "cron", CronExpr: "*/5 * * * *",
		RequestJSON: string(raw), Enabled: 1, NextRunAt: now + 300, CatchUp: 1,
		ProjectKey: "self", TriggerToken: ownerlessToken, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("insert ownerless schedule: %v", err)
	}
	for _, tc := range []struct {
		name  string
		token string
		want  string
	}{
		{name: "the other schedule's owner", token: "tok-alice", want: ownerlessWant},
		{name: "unrelated caller", token: "tok-bob", want: ownerlessWant},
		{name: "root administrator", token: "tok-root", want: ownerlessToken},
	} {
		resp := do(t, s, http.MethodGet, "/v1/schedules/sch-ownerless", tc.token, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s get status=%d, want 200", tc.name, resp.StatusCode)
		}
		var view scheduleView
		decode(t, resp, &view)
		if view.TriggerToken != tc.want {
			t.Fatalf("%s ownerless trigger_token=%q, want %q", tc.name, view.TriggerToken, tc.want)
		}
	}
}

// TestScheduleRunNowUsesTheCurrentCaller (gofer-hxld): run-now runs the stored request
// as the caller that asked for the run, not as the schedule's creator — the job row then
// names who really started it, and that caller's own limits apply. The stored request
// keeps its creator's identity, which is what the cron loop and the webhook use.
func TestScheduleRunNowUsesTheCurrentCaller(t *testing.T) {
	t.Parallel()
	s := scheduleAuthzServer(t)
	created := createWebhookSchedule(t, s, "tok-alice")

	resp := do(t, s, http.MethodPost, "/v1/schedules/"+created.ID+"/run-now", "tok-bob", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run-now status=%d, want 200", resp.StatusCode)
	}
	var res job.JobResult
	decode(t, resp, &res)
	if res.CallerID != "bob" {
		t.Fatalf("run-now job caller_id=%q, want bob (the caller that asked)", res.CallerID)
	}
	waitDoneTok(t, s, res.ID, "tok-bob")

	// The owner still sees its own identity on the stored request.
	resp = do(t, s, http.MethodGet, "/v1/schedules/"+created.ID, "tok-alice", nil)
	var after scheduleView
	decode(t, resp, &after)
	if after.Request.CallerID != "alice" {
		t.Fatalf("stored request caller_id=%q, want alice (the creator)", after.Request.CallerID)
	}

	// The creator's own run-now still names the creator.
	resp = do(t, s, http.MethodPost, "/v1/schedules/"+created.ID+"/run-now", "tok-alice", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner run-now status=%d, want 200", resp.StatusCode)
	}
	var own job.JobResult
	decode(t, resp, &own)
	if own.CallerID != "alice" {
		t.Fatalf("owner run-now caller_id=%q, want alice", own.CallerID)
	}
	waitDoneTok(t, s, own.ID, "tok-alice")
}
