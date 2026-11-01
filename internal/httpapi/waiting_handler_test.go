package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

type waitingCard struct {
	Key          string `json:"key"`
	Kind         string `json:"kind"`
	Waiting      string `json:"waiting"`
	WaitingLabel string `json:"waiting_label"`
}

type todayWaitingResp struct {
	Decisions []waitingCard `json:"decisions"`
	Waiting   []waitingCard `json:"waiting"`
}

func keysSorted(cs []waitingCard) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Key)
	}
	sort.Strings(out)
	return out
}

func seedAgentJob(t *testing.T, s *Server, rec jobstore.JobRecord) {
	t.Helper()
	now := time.Now().Unix()
	rec.ProjectKey, rec.Runner, rec.Cwd = "self", "local", "."
	if rec.Agent == "" {
		rec.Agent = "claude"
	}
	if rec.Channel == "" {
		rec.Channel = "cli"
	}
	rec.ResultDir = t.TempDir()
	rec.StartedAt, rec.UpdatedAt = now-10, now
	if err := s.jobs.Meta().UpsertJob(rec); err != nil {
		t.Fatalf("seed job %s: %v", rec.ID, err)
	}
}

// TestWaitingListIsOneSource: the home page's decision area (/v1/today waiting), the inbox's
// action column and the badge number (total_unread) are the same list, and everything that is
// not a structured waiting fact (work items, a failure of an agent-submitted job) is not in it.
func TestWaitingListIsOneSource(t *testing.T) {
	s := inboxTestServer(t, nil)
	now := time.Now().Unix()
	seedAgentJob(t, s, jobstore.JobRecord{ID: "j-held", Status: "awaiting_approval", EndedAt: 0})
	seedAgentJob(t, s, jobstore.JobRecord{ID: "j-review", Status: "needs_review", EndedAt: now})
	seedAgentJob(t, s, jobstore.JobRecord{ID: "j-own-failed", Status: "failed", Error: "boom", EndedAt: now})
	seedAgentJob(t, s, jobstore.JobRecord{ID: "j-agent-failed", Status: "failed", Error: "boom", EndedAt: now, TagsJSON: `["submitted_by_session:s-1"]`})
	seedAgentJob(t, s, jobstore.JobRecord{ID: "j-cred-failed", Status: "failed", Error: "boom", EndedAt: now, CallerID: "j-held", TagsJSON: `["submitted_by_job:j-held"]`})
	seedAgentJob(t, s, jobstore.JobRecord{ID: "j-cron-failed", Status: "timeout", Error: "late", EndedAt: now, Channel: "cron"})
	// A model-inferred work item that "needs me" is not a waiting fact.
	workCall(t, s, http.MethodPost, "/v1/work-items", "tok-alice", map[string]any{"title": "needs me please", "status": "needs_me"}, 200)

	var td todayWaitingResp
	todayCall(t, s, http.MethodGet, "/v1/today?include_exec=1", "tok-alice", nil, 200, &td)
	in := getInbox(t, s, "tok-alice", "?include_exec=1")

	want := []string{"approval:j-held", "job.terminal:j-own-failed", "review:j-review"}
	if got := keysSorted(td.Waiting); !equalStrings(got, want) {
		t.Fatalf("today waiting = %v, want %v (decisions=%v)", got, want, keysSorted(td.Decisions))
	}
	var inKeys []string
	for _, it := range in.Columns["action"] {
		inKeys = append(inKeys, it.Key)
	}
	sort.Strings(inKeys)
	if !equalStrings(inKeys, want) {
		t.Fatalf("inbox action column = %v, want %v", inKeys, want)
	}
	if in.TotalUnread != len(td.Waiting) || in.Unread["action"] != len(in.Columns["action"]) {
		t.Fatalf("total_unread = %d, unread = %v, list = %d", in.TotalUnread, in.Unread, len(td.Waiting))
	}
	for _, c := range td.Waiting {
		if c.Waiting == "" || c.WaitingLabel == "" {
			t.Errorf("%s has no category: %+v", c.Key, c)
		}
	}
	// Reading an item lowers the unread count and nothing else.
	todayCall(t, s, http.MethodPost, "/v1/inbox/state", "tok-alice", map[string]any{"action": "seen", "keys": []string{"review:j-review"}}, 200, nil)
	in = getInbox(t, s, "tok-alice", "?include_exec=1")
	if in.TotalUnread != len(td.Waiting)-1 || len(in.Columns["action"]) != len(td.Waiting) {
		t.Fatalf("after seen: total_unread=%d list=%d", in.TotalUnread, len(in.Columns["action"]))
	}
	// Agent / credential / schedule failures stay out of every column.
	for _, key := range []string{"job.terminal:j-agent-failed", "job.terminal:j-cred-failed", "job.terminal:j-cron-failed"} {
		if _, ok := in.find(key); ok {
			t.Errorf("%s must not be in the inbox", key)
		}
	}
}

// TestWaitingListHonoursCallerScope: a caller whose ceiling bounds projects sees only the
// waiting items of those projects, in the home list and in the inbox alike.
func TestWaitingListHonoursCallerScope(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "scoped", Token: "tok-scoped", Ceiling: &config.CallerCeiling{Projects: []string{"other"}}},
		},
	})
	seedAgentJob(t, s, jobstore.JobRecord{ID: "j-held", Status: "awaiting_approval"})
	seedAgentJob(t, s, jobstore.JobRecord{ID: "j-own-failed", Status: "failed", Error: "boom", EndedAt: time.Now().Unix()})

	var full, scoped todayWaitingResp
	todayCall(t, s, http.MethodGet, "/v1/today", "tok-alice", nil, 200, &full)
	todayCall(t, s, http.MethodGet, "/v1/today", "tok-scoped", nil, 200, &scoped)
	if len(full.Waiting) != 2 {
		t.Fatalf("unrestricted waiting = %v", keysSorted(full.Waiting))
	}
	if len(scoped.Waiting) != 0 {
		t.Fatalf("scoped waiting = %v, want none", keysSorted(scoped.Waiting))
	}
	if in := getInbox(t, s, "tok-scoped", ""); len(in.Columns["action"]) != 0 || in.TotalUnread != 0 {
		t.Fatalf("scoped inbox = %+v", in)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSubmitStampsAgentSessionProvenance: a user-identity submit that names a terminal agent
// session (the CLI's X-Gofer-Session-Id) is stamped as the agent's, so job.HumanSubmitted
// separates it from the person's own submit. The job is held so nothing runs.
func TestSubmitStampsAgentSessionProvenance(t *testing.T) {
	s := inboxTestServer(t, nil)
	submit := func(withSession bool) jobstore.JobRecord {
		body, _ := json.Marshal(map[string]any{"project_key": "self", "agent": "exec", "cmd": []string{"true"}, "hold": true, "channel": "cli", "runner": "local"})
		req := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok-alice")
		req.Header.Set("Content-Type", "application/json")
		if withSession {
			req.Header.Set(sessionHeader, "sess-1")
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var out struct {
			ID string `json:"id"`
		}
		if rec.Code/100 != 2 || json.NewDecoder(rec.Body).Decode(&out) != nil || out.ID == "" {
			t.Fatalf("submit = %d %s", rec.Code, rec.Body.String())
		}
		row, ok, err := s.jobs.Meta().GetJob(out.ID)
		if err != nil || !ok {
			t.Fatalf("get job: %v %v", ok, err)
		}
		return row
	}
	person, agent := submit(false), submit(true)
	if !job.HumanSubmitted(person) {
		t.Errorf("a submit without a session is the person's: %+v", person)
	}
	if job.HumanSubmitted(agent) || !strings.Contains(agent.TagsJSON, job.TagSubmittedBySession+"sess-1") {
		t.Errorf("a submit from a session must be stamped as the agent's: tags=%s", agent.TagsJSON)
	}
}
