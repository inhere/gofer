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
		first["has_recording"] != true || int(first["cols"].(float64)) != 120 {
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
