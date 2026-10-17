package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/hookrelay"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// TestHeldJobFromSessionWakesTheStopWait is the gofer-fxbu path end to end. A session
// sits at its Stop hook (relay on, waiting_reply) while a held job is submitted
// from "elsewhere" — the shape of a background shell on another project / runner, where
// no source session is possible and the only link back is the notify target the CLI
// defaults to. The job is watched from the moment it is parked, a person approves it,
// it runs and ends, and the Stop wait returns within seconds carrying the job-end notice,
// without anybody replying on the web.
func TestHeldJobFromSessionWakesTheStopWait(t *testing.T) {
	s := newTestServer(t, testToken, false)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	c := client.New(ts.URL, testToken)

	const sid = "fxbu-e2e"
	if _, err := c.RegisterSession(client.SessionRegister{SessionID: sid, Agent: "claude", Runner: "other-worker",
		ProjectKey: "self", Event: "SessionStart"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetSessionRelayMode(sid, client.RelayModeOn); err != nil {
		t.Fatal(err)
	}

	type stopOut struct {
		res hookrelay.Result
		err error
	}
	done := make(chan stopOut, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		res, err := hookrelay.Run(c, hookrelay.Payload{Agent: hookrelay.AgentClaude, Event: "Stop", SessionID: sid,
			LastAssistantMessage: "submitted a held job, waiting"}, hookrelay.Options{Wait: 60 * time.Second, PollSec: 1})
		done <- stopOut{res, err}
	}()
	// Stop components before the DB closes (G051): a failing test must not leave the
	// hook polling a closed server.
	t.Cleanup(func() {
		_, _ = c.SetSessionRelayMode(sid, client.RelayModeOff)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
		}
	})
	wait.Until(t, wait.Timeout(t, 10*time.Second), "the session to wait for a reply", func() bool {
		a, ok, err := s.jobs.Meta().GetAgentSession(sid)
		return err == nil && ok && a.State == jobstore.SessionWaitingReply
	})

	// What `gofer job run --hold` sends from inside the session: no source session
	// (it would be refused here), the session as a notify address.
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: ".",
		TimeoutSec: 30, Title: "held e2e", Hold: true, HoldReason: "needs a person",
		Notify: true, NotifySession: sid,
	})
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("held submit = %d %s", resp.StatusCode, b)
	}
	var held submitResponse
	decode(t, resp, &held)

	// Watched while parked, not only once approved.
	watches, err := s.jobs.Meta().ListSessionJobWatches(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(watches) != 1 || watches[0].JobID != held.ID {
		t.Fatalf("watches while parked = %+v, want the held job %s", watches, held.ID)
	}

	approved := time.Now()
	resp = do(t, s, http.MethodPost, "/v1/jobs/"+held.ID+"/approve", testToken, map[string]string{"note": "ok"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d", resp.StatusCode)
	}
	waitDone(t, s, held.ID)

	select {
	case out := <-done:
		if out.err != nil {
			t.Fatal(out.err)
		}
		if !out.res.Blocked || !strings.Contains(out.res.Reason, held.ID) || !strings.Contains(out.res.Reason, "status=done") {
			t.Fatalf("stop result = %+v, want the job-end notice for %s", out.res, held.ID)
		}
		if took := time.Since(approved); took > 15*time.Second {
			t.Fatalf("the Stop wait took %s after the approval", took)
		}
	case <-time.After(wait.Timeout(t, 15*time.Second)):
		t.Fatal("the Stop wait was not released by the held job ending (needs a web reply)")
	}
}
