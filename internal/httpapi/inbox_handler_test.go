package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

type inboxItemResp struct {
	Key            string `json:"key"`
	Column         string `json:"column"`
	Kind           string `json:"kind"`
	State          string `json:"state"`
	SourceRevision string `json:"source_revision"`
}

type inboxRespBody struct {
	Columns     map[string][]inboxItemResp `json:"columns"`
	Unread      map[string]int             `json:"unread"`
	TotalUnread int                        `json:"total_unread"`
}

func (r inboxRespBody) find(key string) (inboxItemResp, bool) {
	for _, items := range r.Columns {
		for _, it := range items {
			if it.Key == key {
				return it, true
			}
		}
	}
	return inboxItemResp{}, false
}

func inboxTestServer(t *testing.T, ic *config.InboxConfig) *Server {
	t.Helper()
	return newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}, {ID: "bob", Token: "tok-bob"}},
		Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}},
		Inbox:   ic,
	})
}

func seedInboxJob(t *testing.T, s *Server, rec jobstore.JobRecord) {
	t.Helper()
	now := time.Now().Unix()
	rec.ProjectKey, rec.Agent, rec.Runner, rec.Cwd = "self", "exec", "local", "."
	rec.ResultDir = t.TempDir()
	rec.StartedAt, rec.UpdatedAt = now-10, now
	if rec.EndedAt == 0 {
		rec.EndedAt = now
	}
	if err := s.jobs.Meta().UpsertJob(rec); err != nil {
		t.Fatalf("seed job %s: %v", rec.ID, err)
	}
}

func getInbox(t *testing.T, s *Server, token, query string) inboxRespBody {
	t.Helper()
	var out inboxRespBody
	todayCall(t, s, http.MethodGet, "/v1/inbox"+query, token, nil, http.StatusOK, &out)
	return out
}

// A terminal job's column comes from the kind rules, not from a column the handler
// put on the card: a failure nobody continued is "action" (the person decides), a
// success, a user cancel, or a failure already continued is "fyi".
func TestInboxTerminalJobColumns(t *testing.T) {
	s := inboxTestServer(t, nil)
	seedInboxJob(t, s, jobstore.JobRecord{ID: "j-done", Status: "done"})
	seedInboxJob(t, s, jobstore.JobRecord{ID: "j-failed", Status: "failed", Error: "boom"})
	seedInboxJob(t, s, jobstore.JobRecord{ID: "j-timeout", Status: "timeout", Error: "deadline"})
	seedInboxJob(t, s, jobstore.JobRecord{ID: "j-cancelled", Status: "cancelled"})
	seedInboxJob(t, s, jobstore.JobRecord{ID: "j-autoresumed", Status: "failed", Error: "x", AutoResumedBy: "j-next"})
	seedInboxJob(t, s, jobstore.JobRecord{ID: "j-followed", Status: "failed", Error: "x"})
	seedInboxJob(t, s, jobstore.JobRecord{ID: "j-followup", Status: "running", SourceJobID: "j-followed", EndedAt: 0})
	// A failed job that is itself a continuation but has no successor: still needs a decision.
	seedInboxJob(t, s, jobstore.JobRecord{ID: "j-derived-failed", Status: "failed", Error: "again", SourceJobID: "j-gone"})

	got := getInbox(t, s, "tok-alice", "")
	want := map[string]string{
		"job.terminal:j-done":           "fyi",
		"job.terminal:j-failed":         "action",
		"job.terminal:j-timeout":        "action",
		"job.terminal:j-cancelled":      "fyi",
		"job.terminal:j-autoresumed":    "fyi",
		"job.terminal:j-followed":       "fyi",
		"job.terminal:j-derived-failed": "action",
	}
	for key, col := range want {
		it, ok := got.find(key)
		if !ok || it.Column != col {
			t.Errorf("%s = %+v (found %v), want column %s", key, it, ok, col)
		}
	}
	if len(got.Columns["action"]) != 3 || len(got.Columns["fyi"]) != 4 {
		t.Errorf("columns = action %d fyi %d", len(got.Columns["action"]), len(got.Columns["fyi"]))
	}
	if it, _ := got.find("job.terminal:j-failed"); it.Kind != "job.failed" {
		t.Errorf("failed kind = %q", it.Kind)
	}
	if it, _ := got.find("job.terminal:j-done"); it.Kind != "job.terminal" {
		t.Errorf("done kind = %q", it.Kind)
	}
}

// The top-bar badge counts only the "action" column; fyi unread stays in unread.fyi.
func TestInboxTotalUnreadCountsActionOnly(t *testing.T) {
	s := inboxTestServer(t, nil)
	seedInboxJob(t, s, jobstore.JobRecord{ID: "j-done", Status: "done"})
	seedInboxJob(t, s, jobstore.JobRecord{ID: "j-failed", Status: "failed", Error: "boom"})
	got := getInbox(t, s, "tok-alice", "")
	if got.Unread["action"] != 1 || got.Unread["fyi"] != 1 {
		t.Fatalf("unread = %v", got.Unread)
	}
	if got.TotalUnread != 1 {
		t.Fatalf("total_unread = %d, want 1 (action only)", got.TotalUnread)
	}
}

// An explicit rule list is honoured for the job.failed kind too.
func TestInboxCustomRuleMovesFailedJob(t *testing.T) {
	s := inboxTestServer(t, &config.InboxConfig{Rules: []config.InboxRule{
		{ID: "all-fyi", Column: "fyi", Kinds: []string{"job.failed", "job.terminal"}, Priority: 10},
	}})
	seedInboxJob(t, s, jobstore.JobRecord{ID: "j-failed", Status: "failed", Error: "boom"})
	got := getInbox(t, s, "tok-alice", "")
	if it, ok := got.find("job.terminal:j-failed"); !ok || it.Column != "fyi" {
		t.Fatalf("item = %+v ok=%v", it, ok)
	}
	if got.TotalUnread != 0 {
		t.Fatalf("total_unread = %d", got.TotalUnread)
	}
}

func TestInboxDisabledAnswersNotFound(t *testing.T) {
	off := false
	s := inboxTestServer(t, &config.InboxConfig{Enabled: &off})
	todayCall(t, s, http.MethodGet, "/v1/inbox", "tok-alice", nil, http.StatusNotFound, nil)
	todayCall(t, s, http.MethodPost, "/v1/inbox/state", "tok-alice", map[string]any{"action": "seen", "keys": []string{"k"}}, http.StatusNotFound, nil)

	// Rules written without an enabled flag leave the inbox on.
	s2 := inboxTestServer(t, &config.InboxConfig{Rules: []config.InboxRule{{ID: "r", Column: "fyi", Kinds: []string{"job.terminal"}, Priority: 1}}})
	getInbox(t, s2, "tok-alice", "")
}

func TestInboxRefusesWorkerAndJobCredentials(t *testing.T) {
	s := inboxTestServer(t, nil)
	jobToken := seedJobToken(t, s, "job-inbox", jobstore.JobCredentialMember, "")
	for _, tok := range []string{"tok-w1", jobToken} {
		todayCall(t, s, http.MethodGet, "/v1/inbox", tok, nil, http.StatusForbidden, nil)
		todayCall(t, s, http.MethodPost, "/v1/inbox/state", tok, map[string]any{"action": "seen", "keys": []string{"job.terminal:x"}}, http.StatusForbidden, nil)
	}
	todayCall(t, s, http.MethodGet, "/v1/inbox", "", nil, http.StatusUnauthorized, nil)
}

func TestInboxStateIsPerCallerAndRevisionBound(t *testing.T) {
	s := inboxTestServer(t, nil)
	seedInboxJob(t, s, jobstore.JobRecord{ID: "j-failed", Status: "failed", Error: "boom"})
	const key = "job.terminal:j-failed"

	todayCall(t, s, http.MethodPost, "/v1/inbox/state", "tok-alice", map[string]any{"action": "seen", "keys": []string{key}}, 200, nil)
	if it, _ := getInbox(t, s, "tok-alice", "").find(key); it.State != "seen" {
		t.Fatalf("alice state = %q", it.State)
	}
	bob := getInbox(t, s, "tok-bob", "")
	if it, _ := bob.find(key); it.State != "unread" || bob.TotalUnread != 1 {
		t.Fatalf("bob sees alice's read state: %+v total=%d", it, bob.TotalUnread)
	}

	todayCall(t, s, http.MethodPost, "/v1/inbox/state", "tok-alice", map[string]any{"action": "clear", "keys": []string{key}}, 200, nil)
	if _, ok := getInbox(t, s, "tok-alice", "").find(key); ok {
		t.Fatal("cleared item still listed")
	}
	if it, ok := getInbox(t, s, "tok-alice", "?include_cleared=1").find(key); !ok || it.State != "cleared" {
		t.Fatalf("include_cleared = %+v ok=%v", it, ok)
	}
	if _, ok := getInbox(t, s, "tok-bob", "").find(key); !ok {
		t.Fatal("alice's clear hid the item from bob")
	}
	// A column-wide clear by bob does not touch alice's rows either way.
	todayCall(t, s, http.MethodPost, "/v1/inbox/state", "tok-bob", map[string]any{"action": "clear", "column": "action"}, 200, nil)
	if _, ok := getInbox(t, s, "tok-bob", "").find(key); ok {
		t.Fatal("bob's column clear did not hide the item")
	}

	// The source changes (new error text => new revision): alice's cleared item comes back unread.
	seedInboxJob(t, s, jobstore.JobRecord{ID: "j-failed", Status: "failed", Error: "boom again"})
	back, ok := getInbox(t, s, "tok-alice", "").find(key)
	if !ok || back.State != "unread" {
		t.Fatalf("after revision change = %+v ok=%v", back, ok)
	}

	// restore brings a cleared item back to the list.
	todayCall(t, s, http.MethodPost, "/v1/inbox/state", "tok-alice", map[string]any{"action": "clear", "keys": []string{key}}, 200, nil)
	todayCall(t, s, http.MethodPost, "/v1/inbox/state", "tok-alice", map[string]any{"action": "restore", "keys": []string{key}}, 200, nil)
	if _, ok := getInbox(t, s, "tok-alice", "").find(key); !ok {
		t.Fatal("restored item missing")
	}
}

// A batch naming a key that is not in the caller's inbox is refused as a whole:
// nothing is written, so the response and the stored state agree.
func TestInboxStateBatchWithUnknownKeyWritesNothing(t *testing.T) {
	s := inboxTestServer(t, nil)
	seedInboxJob(t, s, jobstore.JobRecord{ID: "j-failed", Status: "failed", Error: "boom"})
	const key = "job.terminal:j-failed"
	todayCall(t, s, http.MethodPost, "/v1/inbox/state", "tok-alice", map[string]any{"action": "seen", "keys": []string{key, "job.terminal:nope"}}, http.StatusNotFound, nil)
	if it, _ := getInbox(t, s, "tok-alice", "").find(key); it.State != "unread" {
		t.Fatalf("partial write: state = %q", it.State)
	}
	var ok struct {
		Updated int `json:"updated"`
	}
	todayCall(t, s, http.MethodPost, "/v1/inbox/state", "tok-alice", map[string]any{"action": "seen", "keys": []string{key, key}}, 200, &ok)
	if ok.Updated != 2 {
		t.Fatalf("updated = %d", ok.Updated)
	}
	todayCall(t, s, http.MethodPost, "/v1/inbox/state", "tok-alice", map[string]any{"action": "bogus", "keys": []string{key}}, 400, nil)
	todayCall(t, s, http.MethodPost, "/v1/inbox/state", "tok-alice", map[string]any{"action": "restore"}, 400, nil)
}

// Without keys, seen / clear act on every current item (optionally one column): the bulk
// "mark all read" and "clear all" buttons, including items the caller never touched before.
func TestInboxStateBulkWithoutKeys(t *testing.T) {
	s := inboxTestServer(t, nil)
	seedInboxJob(t, s, jobstore.JobRecord{ID: "j-done", Status: "done"})
	seedInboxJob(t, s, jobstore.JobRecord{ID: "j-failed", Status: "failed", Error: "boom"})
	var res struct {
		Updated int `json:"updated"`
	}
	todayCall(t, s, http.MethodPost, "/v1/inbox/state", "tok-alice", map[string]any{"action": "seen", "column": "fyi"}, 200, &res)
	got := getInbox(t, s, "tok-alice", "")
	if res.Updated != 1 || got.Unread["fyi"] != 0 || got.Unread["action"] != 1 {
		t.Fatalf("updated=%d unread=%v", res.Updated, got.Unread)
	}
	todayCall(t, s, http.MethodPost, "/v1/inbox/state", "tok-alice", map[string]any{"action": "clear"}, 200, &res)
	got = getInbox(t, s, "tok-alice", "")
	if res.Updated != 2 || len(got.Columns["action"])+len(got.Columns["fyi"]) != 0 {
		t.Fatalf("updated=%d columns=%v", res.Updated, got.Columns)
	}
	todayCall(t, s, http.MethodPost, "/v1/inbox/state", "tok-alice", map[string]any{"action": "clear", "column": "nope"}, 400, nil)
}
