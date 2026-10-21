package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// goferNotices counts the delivered gofer system notices in a session's outbox —
// the durable record a job-end notification leaves behind (operator "gofer").
func goferNotices(t *testing.T, s *Server, sid string) []jobstore.SessionMessage {
	t.Helper()
	list, err := s.jobs.Meta().ReadSessionOutbox(sid)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	var out []jobstore.SessionMessage
	for _, m := range list {
		if m.Operator == "gofer" {
			out = append(out, m)
		}
	}
	return out
}

// TestJobEndNotifyReachesTheSessionThroughTheMessenger is the gofer-3db4 wiring, end
// to end: a job submitted with notify + notify_session ends, the assembled hub hands
// the finished job to the relay, and the session MESSENGER is asked to deliver one
// [gofer job 完成] notice to that session — while a job that never asked to notify
// stays silent.
func TestJobEndNotifyReachesTheSessionThroughTheMessenger(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	msgr := &promptMessenger{}
	s.relay.SetMessenger(msgr)
	cwd := s.projects.Config().Projects["self"].HostPath
	const sid = "job-notify-e2e"
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": sid, "agent": "claude", "project_key": "self", "runner": "local", "cwd": cwd,
		"peer_name": "proj-notify", "peer_messaging": true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register session status=%d, want 200", resp.StatusCode)
	}

	resp = do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: ".",
		Title: "notify e2e", Notify: true, NotifySession: sid,
	})
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		t.Fatalf("notify submit status=%d body=%s", resp.StatusCode, bodyString(t, resp))
	}
	var notified job.JobResult
	decode(t, resp, &notified)

	// A second job WITHOUT notify: it must not reach the messenger at all.
	resp = do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: ".",
		Title: "quiet e2e",
	})
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		t.Fatalf("quiet submit status=%d, want 200 or 202", resp.StatusCode)
	}
	var quiet job.JobResult
	decode(t, resp, &quiet)

	waitDone(t, s, notified.ID)
	waitDone(t, s, quiet.ID)

	// The notice is dispatched off the job's own goroutine, so wait for it.
	wait.Until(t, wait.Timeout(t, 10*time.Second), "the job-end notice to reach the messenger", func() bool {
		return len(goferNotices(t, s, sid)) == 1
	})
	// The outbox row is written before the messenger is called, so wait for the
	// messenger too rather than reading it right after the row appears (G052).
	wait.Until(t, wait.Timeout(t, 10*time.Second), "the messenger to receive the notice", func() bool {
		return strings.Contains(msgr.delivered(), "[gofer job 完成]")
	})
	delivered := msgr.delivered()
	for _, want := range []string{"[gofer job 完成]", notified.ID, "notify e2e", "status=done", "gofer job show " + notified.ID} {
		if !strings.Contains(delivered, want) {
			t.Fatalf("delivered notice %q does not contain %q", delivered, want)
		}
	}
	if strings.Contains(delivered, quiet.ID) {
		t.Fatalf("the quiet job reached the messenger: %q", delivered)
	}
	// The delivered notice claimed the pending record, so the Stop hook and its
	// catch-up cannot deliver the same job's notice a second time.
	watches, err := s.jobs.Meta().ListSessionJobWatches(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(watches) != 0 {
		t.Fatalf("the claimed watch is still there: %+v", watches)
	}
	if n := len(goferNotices(t, s, sid)); n != 1 {
		t.Fatalf("gofer notices = %d, want exactly 1", n)
	}
	if others := goferNotices(t, s, quiet.ID); len(others) != 0 {
		t.Fatalf("a job without notify left notices behind: %+v", others)
	}
}

// TestScheduleRunNowDropsAnUnusableNotifyTarget: a schedule whose stored request asks
// to notify a session that has since gone must still RUN (gofer-3db4). The run is
// gofer's own trigger, so the notification is dropped and the job's timeline records
// job.notify_dropped with the reason — refusing the run would lose work nobody asked
// about because a notice could not be addressed.
func TestScheduleRunNowDropsAnUnusableNotifyTarget(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	const gone = "scheduled-session-gone"
	create := do(t, s, http.MethodPost, "/v1/schedules", testToken, createScheduleReq{
		Name: "notify-drop", Cron: "*/5 * * * *",
		Request: job.JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"},
			Cwd: ".", TimeoutSec: 30, Title: "scheduled notify",
			Notify: true, NotifySession: gone,
		},
	})
	if create.StatusCode != http.StatusOK {
		t.Fatalf("create schedule status=%d, want 200", create.StatusCode)
	}
	var created scheduleView
	decode(t, create, &created)

	run := do(t, s, http.MethodPost, "/v1/schedules/"+created.ID+"/run-now", testToken, nil)
	if run.StatusCode != http.StatusOK {
		t.Fatalf("run-now status=%d, want 200 (an unusable notify target must not refuse the run)", run.StatusCode)
	}
	var res job.JobResult
	decode(t, run, &res)
	waitDone(t, s, res.ID)
	if res.Notify || res.NotifySession != "" {
		t.Fatalf("the dropped notification is still on the job: notify=%v session=%q", res.Notify, res.NotifySession)
	}
	assertNotifyDropped(t, s, res.ID, gone)
}

// assertNotifyDropped fails unless the job's timeline carries one job.notify_dropped
// event naming target, with a reason that says why the notice could not be sent.
func assertNotifyDropped(t *testing.T, s *Server, jobID, target string) {
	t.Helper()
	events, err := s.jobs.ListJobEvents(jobID, 0)
	if err != nil {
		t.Fatalf("list job events: %v", err)
	}
	for _, e := range events {
		if e.Type != job.EventNotifyDropped {
			continue
		}
		if !strings.Contains(e.Detail, target) || !strings.Contains(e.Detail, "reason") {
			t.Fatalf("notify_dropped detail = %s, want the target %q and a reason", e.Detail, target)
		}
		return
	}
	t.Fatalf("no %s event on job %s (%d events)", job.EventNotifyDropped, jobID, len(events))
}

// TestNotifyingAnUnknownSessionIsRefusedAtSubmit: the HTTP surface carries the same
// refusal as the CLI — a notify target this server does not know has no address, and
// the request is a 400 rather than a job that could never tell anyone.
func TestNotifyingAnUnknownSessionIsRefusedAtSubmit(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: ".",
		Notify: true, NotifySession: "no-such-session",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("notify with an unknown session status=%d, want 400", resp.StatusCode)
	}
	jobs, err := s.jobs.ListJobs(job.ListOpts{Project: "self"})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("a refused notify created %d job(s)", len(jobs))
	}
}
