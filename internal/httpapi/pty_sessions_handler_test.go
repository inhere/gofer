package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func getPtySessions(t *testing.T, s *Server, jobID, token string) (*http.Response, map[string]any) {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/jobs/"+jobID+"/pty/sessions", token, nil)
	var body map[string]any
	if resp.StatusCode == http.StatusOK {
		decode(t, resp, &body)
	} else {
		resp.Body.Close()
	}
	return resp, body
}

func TestPtySessionsOwnerViewHidesSensitiveFields(t *testing.T) {
	s := recordingServer(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	dir := addRecordingJob(t, s, "job-sessions", "alice", "")
	rec, ok, err := s.jobs.Meta().GetJob("job-sessions")
	if err != nil || !ok {
		t.Fatalf("get recording job: ok=%v err=%v", ok, err)
	}
	rec.SessionID = "sid-pty-sessions"
	if err := s.jobs.Meta().UpsertJob(rec); err != nil {
		t.Fatalf("upsert recording job session id: %v", err)
	}
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertPtySession(jobstore.PtySessionRecord{
		PtySessionID: "pty-old", JobID: "job-sessions", WorkerID: "w1", InstanceID: "inst-secret",
		Owner: "alice", State: "closed", Cols: 100, Rows: 30, RecordingURI: "",
		Encrypted: 2, BytesIn: 5, BytesOut: 10, StartedAt: now - 20, EndedAt: now - 10,
	}); err != nil {
		t.Fatalf("upsert old pty session: %v", err)
	}
	if err := s.jobs.Meta().UpsertPtySession(jobstore.PtySessionRecord{
		PtySessionID: "pty-new", JobID: "job-sessions", WorkerID: "w1", InstanceID: "inst-secret",
		Owner: "alice", State: "closed", Cols: 120, Rows: 40, RecordingURI: filepath.Join(dir, "pty.cast"),
		Encrypted: 1, BytesIn: 7, BytesOut: 12, StartedAt: now, EndedAt: now + 1,
	}); err != nil {
		t.Fatalf("upsert new pty session: %v", err)
	}

	resp, body := getPtySessions(t, s, "job-sessions", "tok-alice")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	sessions, ok := body["sessions"].([]any)
	if !ok || len(sessions) != 2 {
		t.Fatalf("sessions=%#v, want 2 rows", body["sessions"])
	}
	first := sessions[0].(map[string]any)
	if first["pty_session_id"] != "pty-new" || first["encrypted"] != true ||
		first["has_recording"] != true || first["session_id"] != "sid-pty-sessions" ||
		int(first["cols"].(float64)) != 120 {
		t.Fatalf("first session = %#v", first)
	}
	second := sessions[1].(map[string]any)
	if second["pty_session_id"] != "pty-old" || second["encrypted"] != false ||
		second["has_recording"] != false {
		t.Fatalf("second session = %#v", second)
	}

	raw, _ := json.Marshal(body)
	for _, forbidden := range []string{dir, "inst-secret", "alice", "recording_uri"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("response leaked %q: %s", forbidden, raw)
		}
	}
}

func TestPtySessionsAuthUnknownAndNilStore(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
			{ID: "admin", Token: "tok-admin", CanAdmin: true},
		},
	})
	addRecordingJob(t, s, "job-a", "alice", "")

	resp, body := getPtySessions(t, s, "job-a", "tok-alice")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("nil-store owner status=%d, want 200", resp.StatusCode)
	}
	if sessions, ok := body["sessions"].([]any); !ok || len(sessions) != 0 {
		t.Fatalf("nil-store sessions=%#v, want empty array", body["sessions"])
	}

	resp, _ = getPtySessions(t, s, "job-a", "tok-bob")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owner status=%d, want 403", resp.StatusCode)
	}
	resp, _ = getPtySessions(t, s, "missing", "tok-admin")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown status=%d, want 404", resp.StatusCode)
	}
}

func getRecentPtySessions(t *testing.T, s *Server, token, query string) (*http.Response, map[string]any) {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/pty/sessions"+query, token, nil)
	var body map[string]any
	if resp.StatusCode == http.StatusOK {
		decode(t, resp, &body)
	} else {
		resp.Body.Close()
	}
	return resp, body
}

func TestRecentPtySessionsOwnerAdminAndLimit(t *testing.T) {
	s := recordingServer(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
			{ID: "admin", Token: "tok-admin", CanAdmin: true},
		},
	})
	dirA := addRecordingJob(t, s, "job-a", "alice", "")
	dirB := addRecordingJob(t, s, "job-b", "bob", "")
	now := time.Now().Unix()
	rows := []jobstore.PtySessionRecord{
		{PtySessionID: "pty-a-old", JobID: "job-a", Owner: "alice", State: "closed",
			Cols: 80, Rows: 24, RecordingURI: "", Encrypted: 2, StartedAt: now - 30},
		{PtySessionID: "pty-b-new", JobID: "job-b", Owner: "bob", State: "closed",
			Cols: 100, Rows: 30, RecordingURI: filepath.Join(dirB, "pty.cast"), Encrypted: 1, StartedAt: now - 10},
		{PtySessionID: "pty-a-new", JobID: "job-a", Owner: "alice", State: "closed",
			Cols: 120, Rows: 40, RecordingURI: filepath.Join(dirA, "pty.cast"), Encrypted: 1, StartedAt: now},
	}
	for _, r := range rows {
		if err := s.jobs.Meta().UpsertPtySession(r); err != nil {
			t.Fatalf("upsert recent row %s: %v", r.PtySessionID, err)
		}
	}

	resp, body := getRecentPtySessions(t, s, "tok-alice", "?limit=200")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner status=%d, want 200", resp.StatusCode)
	}
	sessions := body["sessions"].([]any)
	if len(sessions) != 2 {
		t.Fatalf("alice sessions=%#v, want 2 own rows", sessions)
	}
	first := sessions[0].(map[string]any)
	if first["job_id"] != "job-a" || first["pty_session_id"] != "pty-a-new" {
		t.Fatalf("alice first session=%#v", first)
	}
	for _, sess := range sessions {
		if sess.(map[string]any)["job_id"] == "job-b" {
			t.Fatalf("non-admin saw bob session: %#v", sessions)
		}
	}

	resp, body = getRecentPtySessions(t, s, "tok-admin", "?limit=2")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin status=%d, want 200", resp.StatusCode)
	}
	sessions = body["sessions"].([]any)
	if len(sessions) != 2 {
		t.Fatalf("admin sessions=%#v, want limit 2", sessions)
	}
	if sessions[0].(map[string]any)["pty_session_id"] != "pty-a-new" ||
		sessions[1].(map[string]any)["pty_session_id"] != "pty-b-new" {
		t.Fatalf("admin sessions order=%#v", sessions)
	}

	resp, body = getRecentPtySessions(t, s, "tok-admin", "?limit=999")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin cap status=%d, want 200", resp.StatusCode)
	}
	sessions = body["sessions"].([]any)
	if len(sessions) != 3 {
		t.Fatalf("admin capped sessions=%#v, want all 3 below cap", sessions)
	}

	raw, _ := json.Marshal(body)
	for _, forbidden := range []string{dirA, dirB, "alice", "bob", "recording_uri"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("recent response leaked %q: %s", forbidden, raw)
		}
	}
}
