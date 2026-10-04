package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

type failingPtyStore struct{ PtySessionStore }

func (failingPtyStore) ListPtySessionsByJob(string) ([]jobstore.PtySessionRecord, error) {
	return nil, errors.New("pty store down")
}

func addIncludeJob(t *testing.T, s *Server, id, caller, status, sessionID string) {
	t.Helper()
	now := time.Now().Unix()
	rec := jobstore.JobRecord{
		ID: id, ProjectKey: "self", Agent: "exec", Runner: "local", Status: status,
		Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, UpdatedAt: now, CallerID: caller,
		SessionID: sessionID,
	}
	if status == "done" {
		rec.EndedAt = now
		rec.ArtifactsJSON = `[{"name":"a.txt","size":1,"mtime":1},{"name":"b.txt","size":2,"mtime":2}]`
	}
	if err := s.jobs.Meta().UpsertJob(rec); err != nil {
		t.Fatalf("upsert job: %v", err)
	}
}

func getInclude(t *testing.T, s *Server, id, query, token string) (int, map[string]any) {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/jobs/"+id+query, token, nil)
	var body map[string]any
	if resp.StatusCode == http.StatusOK {
		decode(t, resp, &body)
	} else {
		resp.Body.Close()
	}
	return resp.StatusCode, body
}

func TestJobDetailInclude(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice", CanAttach: true},
			{ID: "bob", Token: "tok-bob"},
		},
		Governance: config.GovernanceConfig{RequireAttachCapability: true},
	})
	st := s.jobs.Meta()
	addIncludeJob(t, s, "job-run", "alice", "running", "sess-1")
	addIncludeJob(t, s, "job-prev", "alice", "done", "sess-1")
	addIncludeJob(t, s, "job-done", "alice", "done", "")

	// Plain detail carries none of the include keys.
	code, body := getInclude(t, s, "job-run", "", "tok-alice")
	if code != 200 {
		t.Fatalf("plain status=%d", code)
	}
	for _, k := range []string{"events", "comments", "deliveries", "retries", "wakeups", "pty_sessions", "artifacts", "session_jobs", "include_errors"} {
		if _, ok := body[k]; ok {
			t.Fatalf("plain detail unexpectedly has %q", k)
		}
	}

	// Seed side data.
	for i := 0; i < 250; i++ {
		if _, err := st.InsertJobEvent(jobstore.JobEvent{JobID: "job-run", Type: "job.test", At: int64(1000 + i)}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 105; i++ {
		if err := st.InsertComment(jobstore.Comment{
			ID: "c" + strconv.Itoa(1000+i), Scope: jobstore.CommentScopeJob, ScopeID: "job-run",
			Author: "alice", AuthorKind: jobstore.CommentAuthorUser, Body: "b" + strconv.Itoa(i), CreatedAt: int64(2000 + i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.InsertDelivery(jobstore.Delivery{JobID: "job-run", EventSeq: 1, Target: "hook", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertRetry(jobstore.RetryRecord{ID: "r1", SourceJobID: "job-run", Attempt: 2, RequestJSON: "{}", State: "pending", NextRunAt: 1, CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertWakeup(jobstore.WakeupRecord{ID: "w1", JobID: "job-run", Kind: "timer", Mode: "notify", Enabled: 1, CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}

	all := "?include=events,comments,deliveries,retries,wakeups,pty_sessions,artifacts,session_jobs"
	code, body = getInclude(t, s, "job-run", all, "tok-alice")
	if code != 200 {
		t.Fatalf("include status=%d", code)
	}
	evs, _ := body["events"].([]any)
	if len(evs) != 200 || body["events_truncated"] != true {
		t.Fatalf("events len=%d truncated=%v, want 200/true", len(evs), body["events_truncated"])
	}
	first := evs[0].(map[string]any)["seq"].(float64)
	last := evs[len(evs)-1].(map[string]any)["seq"].(float64)
	if first >= last || body["events_last_seq"].(float64) != last {
		t.Fatalf("events not ascending / last_seq mismatch: first=%v last=%v last_seq=%v", first, last, body["events_last_seq"])
	}
	// before= pages the older remainder.
	_, older := getInclude(t, s, "job-run", "?include=events&before="+strconv.Itoa(int(first)), "tok-alice")
	oev, _ := older["events"].([]any)
	if len(oev) == 0 || oev[len(oev)-1].(map[string]any)["seq"].(float64) >= first {
		t.Fatalf("before paging returned %d events", len(oev))
	}
	if cm, _ := body["comments"].([]any); len(cm) != 100 || body["comments_total"].(float64) != 105 {
		t.Fatalf("comments len=%d total=%v, want 100/105", len(cm), body["comments_total"])
	}
	if d, _ := body["deliveries"].([]any); len(d) != 1 {
		t.Fatalf("deliveries=%v", body["deliveries"])
	}
	if r, _ := body["retries"].([]any); len(r) != 1 {
		t.Fatalf("retries=%v", body["retries"])
	}
	if w, _ := body["wakeups"].([]any); len(w) != 1 {
		t.Fatalf("wakeups=%v", body["wakeups"])
	}
	if _, ok := body["pty_sessions"]; ok {
		// no store wired in this server -> empty list omitted, not an error
		t.Fatalf("pty_sessions unexpectedly present: %v", body["pty_sessions"])
	}
	if _, ok := body["artifacts"]; ok {
		t.Fatalf("running job must not inline artifacts: %v", body["artifacts"])
	}
	if sj, _ := body["session_jobs"].([]any); len(sj) != 2 {
		t.Fatalf("session_jobs=%v, want job-prev + job-run", body["session_jobs"])
	}

	// Terminal job inlines its manifest with a total.
	_, done := getInclude(t, s, "job-done", "?include=artifacts", "tok-alice")
	if a, _ := done["artifacts"].([]any); len(a) != 2 || done["artifacts_total"].(float64) != 2 {
		t.Fatalf("artifacts=%v total=%v", done["artifacts"], done["artifacts_total"])
	}

	// Unknown token -> 400; unknown job -> 404.
	if code, _ := getInclude(t, s, "job-run", "?include=events,nope", "tok-alice"); code != 400 {
		t.Fatalf("unknown include status=%d, want 400", code)
	}
	if code, _ := getInclude(t, s, "ghost", "?include=events", "tok-alice"); code != 404 {
		t.Fatalf("unknown job status=%d, want 404", code)
	}

	// pty: a caller without attach rights gets the field left out, flagged forbidden;
	// everything else is still served.
	code, body = getInclude(t, s, "job-run", "?include=pty_sessions,retries", "tok-bob")
	if code != 200 {
		t.Fatalf("bob status=%d", code)
	}
	errs, _ := body["include_errors"].(map[string]any)
	if errs["pty_sessions"] != "forbidden" || body["pty_sessions"] != nil {
		t.Fatalf("pty forbidden not reported: errs=%v pty=%v", errs, body["pty_sessions"])
	}
	if r, _ := body["retries"].([]any); len(r) != 1 {
		t.Fatalf("other parts must still be served: %v", body["retries"])
	}

	// One failing part is reported on its own and does not fail the response.
	s.SetPtySessionStore(failingPtyStore{})
	code, body = getInclude(t, s, "job-run", "?include=pty_sessions,wakeups", "tok-alice")
	if code != 200 {
		t.Fatalf("failing part status=%d, want 200", code)
	}
	errs, _ = body["include_errors"].(map[string]any)
	if errs["pty_sessions"] != "pty store down" {
		t.Fatalf("include_errors=%v", errs)
	}
	if w, _ := body["wakeups"].([]any); len(w) != 1 {
		t.Fatalf("healthy part lost: %v", body["wakeups"])
	}
}
