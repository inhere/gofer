package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// TestScheduleWebhookTriggerToken (AUTO-02b): a schedule can carry its OWN webhook
// secret, and `POST /v1/schedules/{id}/trigger` runs it for a caller that has only that
// token — no gofer bearer. A wrong (or absent) token is 401, an unknown schedule 404,
// the run records schedule.triggered on the job it started, and a repeat inside the
// 10s window is 429 instead of a second job.
func TestScheduleWebhookTriggerToken(t *testing.T) {
	s := newTestServer(t, testToken, false)

	createResp := do(t, s, http.MethodPost, "/v1/schedules", testToken, createScheduleReq{
		Name: "webhook-nightly",
		Cron: "*/5 * * * *",
		Request: job.JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		},
		Webhook: true,
	})
	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", createResp.StatusCode)
	}
	var created scheduleView
	decode(t, createResp, &created)
	if created.TriggerToken == "" {
		t.Fatal("--webhook did not mint a trigger token")
	}
	if len(created.TriggerToken) != 32 {
		t.Fatalf("trigger token = %q, want 24 bytes of base64url (32 chars)", created.TriggerToken)
	}

	// A schedule created WITHOUT the webhook has no token and cannot be triggered.
	plainResp := do(t, s, http.MethodPost, "/v1/schedules", testToken, createScheduleReq{
		Name: "no-webhook",
		Cron: "*/5 * * * *",
		Request: job.JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		},
	})
	if plainResp.StatusCode != http.StatusOK {
		t.Fatalf("create plain status=%d, want 200", plainResp.StatusCode)
	}
	var plain scheduleView
	decode(t, plainResp, &plain)
	if plain.TriggerToken != "" {
		t.Fatalf("a schedule without --webhook got token %q", plain.TriggerToken)
	}
	off := do(t, s, http.MethodPost, "/v1/schedules/"+plain.ID+"/trigger?token=whatever", "", nil)
	if off.StatusCode != http.StatusUnauthorized {
		t.Fatalf("trigger on a webhook-less schedule status=%d, want 401", off.StatusCode)
	}
	off.Body.Close()

	// The token is the ONLY credential: no bearer is sent on any of these.
	bad := do(t, s, http.MethodPost, "/v1/schedules/"+created.ID+"/trigger?token=wrong", "", nil)
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token status=%d, want 401", bad.StatusCode)
	}
	bad.Body.Close()

	missing := do(t, s, http.MethodPost, "/v1/schedules/"+created.ID+"/trigger", "", nil)
	if missing.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d, want 401", missing.StatusCode)
	}
	missing.Body.Close()

	unknown := do(t, s, http.MethodPost, "/v1/schedules/sch-nope/trigger?token="+created.TriggerToken, "", nil)
	if unknown.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown schedule status=%d, want 404", unknown.StatusCode)
	}
	unknown.Body.Close()

	// The header form works as well as the query parameter.
	okResp := do(t, s, http.MethodPost, "/v1/schedules/"+created.ID+"/trigger", "", nil)
	if okResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credential status=%d, want 401", okResp.StatusCode)
	}
	okResp.Body.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/schedules/"+created.ID+"/trigger", nil)
	req.Header.Set("X-Gofer-Trigger-Token", created.TriggerToken)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("header trigger status=%d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var submitted job.JobResult
	if err := json.Unmarshal(rec.Body.Bytes(), &submitted); err != nil {
		t.Fatalf("decode trigger response: %v", err)
	}
	if submitted.ID == "" || submitted.Channel != "webhook" {
		t.Fatalf("triggered job = %+v, want a job with channel=webhook", submitted)
	}
	events, err := s.jobs.ListJobEvents(submitted.ID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	found := false
	for _, ev := range events {
		if ev.Type == job.EventScheduleTriggered {
			found = true
			var detail map[string]any
			if err := json.Unmarshal([]byte(ev.Detail), &detail); err != nil {
				t.Fatalf("event detail %q: %v", ev.Detail, err)
			}
			if detail["source"] != "webhook" {
				t.Fatalf("schedule.triggered detail = %v, want source=webhook", detail)
			}
		}
	}
	if !found {
		t.Fatalf("job %s has no %s event: %+v", submitted.ID, job.EventScheduleTriggered, events)
	}

	// A storm is refused: the same schedule may not be triggered again inside 10s.
	again := do(t, s, http.MethodPost, "/v1/schedules/"+created.ID+"/trigger?token="+created.TriggerToken, "", nil)
	if again.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("immediate repeat status=%d, want 429", again.StatusCode)
	}
	again.Body.Close()

	// The rate limit is per schedule: the OTHER schedule (with its own token) is not
	// affected by this one's window.
	rotateResp := do(t, s, http.MethodPost, "/v1/schedules/"+plain.ID+"/rotate-token", testToken, nil)
	if rotateResp.StatusCode != http.StatusOK {
		t.Fatalf("rotate-token status=%d, want 200", rotateResp.StatusCode)
	}
	var rotated scheduleView
	decode(t, rotateResp, &rotated)
	if rotated.TriggerToken == "" || rotated.TriggerToken == created.TriggerToken {
		t.Fatalf("rotated token = %q, want a fresh one", rotated.TriggerToken)
	}
	// The old token is dead for the rotated schedule, and the new one works.
	stale := do(t, s, http.MethodPost, "/v1/schedules/"+plain.ID+"/trigger?token=whatever", "", nil)
	if stale.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stale token status=%d, want 401", stale.StatusCode)
	}
	stale.Body.Close()
	fresh := do(t, s, http.MethodPost, "/v1/schedules/"+plain.ID+"/trigger?token="+rotated.TriggerToken, "", nil)
	if fresh.StatusCode != http.StatusOK {
		t.Fatalf("fresh token status=%d, want 200", fresh.StatusCode)
	}
	fresh.Body.Close()

	// rotate-token on an unknown schedule is 404.
	missRotate := do(t, s, http.MethodPost, "/v1/schedules/sch-nope/rotate-token", testToken, nil)
	if missRotate.StatusCode != http.StatusNotFound {
		t.Fatalf("rotate unknown status=%d, want 404", missRotate.StatusCode)
	}
	missRotate.Body.Close()

	// The token round-trips through the authenticated read, which is where an operator
	// finds it again.
	showResp := do(t, s, http.MethodGet, "/v1/schedules/"+created.ID, testToken, nil)
	if showResp.StatusCode != http.StatusOK {
		t.Fatalf("get schedule status=%d, want 200", showResp.StatusCode)
	}
	var shown scheduleView
	decode(t, showResp, &shown)
	if shown.TriggerToken != created.TriggerToken {
		t.Fatalf("show trigger_token = %q, want %q", shown.TriggerToken, created.TriggerToken)
	}
}
