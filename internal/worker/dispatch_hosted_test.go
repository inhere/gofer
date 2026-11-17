package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/wsproto"
)

// TestDispatchHostedReachesLocalSubmit: the dispatch's hosted flag is what keeps this
// machine's own Submit from adding the idle default to a hosted session; a dispatch
// without it leaves the flag off.
func TestDispatchHostedReachesLocalSubmit(t *testing.T) {
	for _, hosted := range []bool{true, false} {
		jobs := &stubJobs{
			submitID: "local-1", getResult: job.JobResult{Status: job.StatusDone}, getOK: true,
			waitResult: job.JobResult{Status: job.StatusDone}, waitOK: true,
		}
		cl, _, url := dialLiveClient(t, jobs)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cl.handleDispatch(ctx, url, wsproto.Dispatch{JobID: "d1", Session: true, Hosted: hosted})
		cancel()
		jobs.mu.Lock()
		got := jobs.gotHosted
		jobs.mu.Unlock()
		if got != hosted {
			t.Fatalf("dispatch hosted=%v reached Submit as %v", hosted, got)
		}
	}
}

// TestSessionEndReasonTravelsOnTheOutcomeFrame: the worker's own reason the session ended
// rides the outcome frame (and alone justifies sending one), so the host row records it.
func TestSessionEndReasonTravelsOnTheOutcomeFrame(t *testing.T) {
	o, send := outcomeFrame("hub-1", job.JobResult{SessionEndReason: "idle_timeout"})
	if !send || o.SessionEndReason != "idle_timeout" {
		t.Fatalf("outcome send=%v reason=%q", send, o.SessionEndReason)
	}
	b, _ := json.Marshal(wsproto.Outcome{JobID: "j"})
	if strings.Contains(string(b), "session_end_reason") {
		t.Fatalf("an outcome without a reason grew the key: %s", b)
	}
	if _, send := outcomeFrame("hub-2", job.JobResult{}); send {
		t.Fatal("an empty result must not send an outcome frame")
	}
}
