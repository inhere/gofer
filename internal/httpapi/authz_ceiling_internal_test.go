package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/work"
)

// Triggering routes of internal jobs under a ceiling (plan S7): a restricted caller cannot
// make gofer start a relay / delivery / sync / tidy-up job for something outside its
// projects, and the jobs it can trigger carry its ceiling.

// TestCeilingBlocksTriggeringInternalJobsOutOfScope: every route that makes gofer submit a
// job on its own behalf is a 404 for a resource of another project, and a 403 where the
// route has no project at all.
func TestCeilingBlocksTriggeringInternalJobsOutOfScope(t *testing.T) {
	s := newCeilingServer(t)
	seedCeilingWorld(t, s, "p1")
	out := seedCeilingWorld(t, s, "p2")
	grantAll(s)

	outOfScope := []ceilingWrite{
		{"relay: send a message", "POST", "/v1/sessions/" + out.sessionID + "/messages", map[string]any{"text": "hi"}},
		{"relay: deliver", "POST", "/v1/sessions/" + out.sessionID + "/deliver", map[string]any{"text": "hi"}},
		{"relay: take over / resume", "POST", "/v1/sessions/" + out.sessionID + "/resume", map[string]any{}},
		{"relay: say", "POST", "/v1/sessions/" + out.sessionID + "/say", map[string]any{"answer": "x"}},
		{"relay: nudge", "POST", "/v1/sessions/" + out.sessionID + "/nudges", map[string]any{"text": "x", "every_sec": 60}},
		{"relay: work item dispatch", "POST", "/v1/work-items/" + out.workItemID + "/dispatch", map[string]any{}},
		{"relay: ask a session", "POST", "/v1/session-ask", map[string]any{"session_id": out.sessionID, "text": "hi"}},
		{"delivery of a job", "POST", "/v1/jobs/" + out.jobID + "/deliver", map[string]any{}},
		{"tracker repo sync", "POST", "/v1/tracker/repos/" + out.trackerID + "/sync", map[string]any{}},
		{"summarizer", "POST", "/v1/work-items/" + out.workItemID + "/summarize", map[string]any{}},
		{"workflow from a foreign project", "POST", "/v1/workflows", out.bodyFor("POST /v1/workflows")},
	}
	for _, w := range outOfScope {
		status, body := ceilReq(t, s, ceilTokLim, w.method, w.path, w.body)
		if !isGateOutOfScope(status, body) {
			t.Errorf("%s: %s %s = %d (%s), want 404", w.name, w.method, w.path, status, body)
		}
	}
	// No project at all: refused outright for a restricted caller.
	for _, w := range []ceilingWrite{
		{"steward start", "POST", "/v1/steward/start", map[string]any{}},
		{"steward ask", "POST", "/v1/steward/ask", map[string]any{"text": "x"}},
		{"steward review", "POST", "/v1/steward/review", map[string]any{}},
		{"agent probe", "POST", "/v1/agents/exec/probe", map[string]any{}},
		{"runner messenger listing", "GET", "/v1/runners/local/messenger/agents", nil},
	} {
		status, body := ceilReq(t, s, ceilTokLim, w.method, w.path, w.body)
		if status != http.StatusForbidden || !strings.Contains(body, "caller ceiling") {
			t.Errorf("%s: %s %s = %d (%s), want the ceiling 403", w.name, w.method, w.path, status, body)
		}
	}
}

// TestCeilingJudgesTheInternalJobsItTriggers: an in-scope trigger is not stopped by the
// route, but the job it submits carries the caller as ceiling owner — the tracker-sync job of
// a caller whose ceiling lacks the machine runner is refused by admission (403), which it
// could not be if the job were an owner-less system job.
func TestCeilingJudgesTheInternalJobsItTriggers(t *testing.T) {
	s := newCeilingServer(t)
	in := seedCeilingWorld(t, s, "p1")
	grantAll(s)
	status, body := ceilReq(t, s, ceilTokLim, "POST", "/v1/tracker/repos/"+in.trackerID+"/sync", map[string]any{})
	if status != http.StatusForbidden || !strings.Contains(body, "caller ceiling denied") {
		t.Errorf("in-scope tracker sync of a caller without the machine runner = %d (%s), want the admission 403", status, body)
	}
}

// TestInternalJobRequestsCarryTheirOwner pins the request builders of the HTTP-side
// internal jobs: the summarizer job takes the owner of the tidy-up request and the marker.
func TestInternalJobRequestsCarryTheirOwner(t *testing.T) {
	t.Parallel()
	req := summarizerJobRequest(work.OneShotRequest{Agent: "a", ProjectKey: "p1", Origin: "lim"})
	if req.OriginCaller != "lim" || req.InternalExec != job.InternalExecSummarizer {
		t.Errorf("summarizer request owner/marker = %q/%q", req.OriginCaller, req.InternalExec)
	}
	if got := summarizerJobRequest(work.OneShotRequest{Agent: "a", ProjectKey: "p1"}); got.OriginCaller != "" {
		t.Errorf("a timer-driven tidy-up has no owner, got %q", got.OriginCaller)
	}
}
