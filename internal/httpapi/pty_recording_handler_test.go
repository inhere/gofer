package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/castrec"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// recordingServer builds a test server with the pty-session store wired (the
// recording gate reads through it).
func recordingServer(t *testing.T, sc config.ServerConfig) *Server {
	t.Helper()
	s := newTestServerCfg(t, sc)
	s.SetPtySessionStore(s.jobs.Meta())
	return s
}

// addRecordingJob upserts an interactive job (owner=caller, execution source) and
// returns its result dir.
func addRecordingJob(t *testing.T, s *Server, jobID, caller, source string) string {
	t.Helper()
	dir := t.TempDir()
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: jobID, ProjectKey: "self", Agent: "exec", Runner: "worker", Interactive: true,
		Status: "running", Cwd: ".", ResultDir: dir, StartedAt: now, UpdatedAt: now,
		CallerID: caller, Source: source,
	}); err != nil {
		t.Fatalf("upsert recording job: %v", err)
	}
	return dir
}

// addPtyRow upserts a closed pty_sessions row for jobID.
func addPtyRow(t *testing.T, s *Server, jobID, owner, uri string, encrypted int) {
	t.Helper()
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertPtySession(jobstore.PtySessionRecord{
		PtySessionID: "pty-" + jobID, JobID: jobID, Owner: owner, State: "closed",
		RecordingURI: uri, Encrypted: encrypted, BytesOut: 42, StartedAt: now, EndedAt: now,
	}); err != nil {
		t.Fatalf("upsert pty row: %v", err)
	}
}

// writeEncryptedCast produces a real encrypted cast file at path (via the
// recorder's sink) containing payload, returning the recorder for the handler and
// the on-disk bytes for corruption tests.
func writeEncryptedCast(t *testing.T, path, payload string) *castrec.Recorder {
	t.Helper()
	rec, err := castrec.New(config.CastConfig{
		Enabled:    true,
		Encryption: config.CastEncryptionConfig{Enabled: true},
	}, bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatalf("new encrypted recorder: %v", err)
	}
	sink, err := rec.Open(path, 80, 24, time.Now().Unix())
	if err != nil {
		t.Fatalf("open encrypted sink: %v", err)
	}
	if _, err := sink.Write([]byte(payload)); err != nil {
		t.Fatalf("write cast: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("close cast: %v", err)
	}
	return rec
}

func getRecording(t *testing.T, s *Server, jobID, token string) *http.Response {
	t.Helper()
	return do(t, s, http.MethodGet, "/v1/jobs/"+jobID+"/pty/recording", token, nil)
}

func TestPtyRecordingOwnerPlaintext(t *testing.T) {
	s := recordingServer(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	dir := addRecordingJob(t, s, "job-rec", "alice", "")
	body := []byte("{\"version\":2,\"width\":80,\"height\":24}\n[0.10,\"o\",\"hi\"]\n")
	castPath := filepath.Join(dir, "pty.cast")
	if err := os.WriteFile(castPath, body, 0o600); err != nil {
		t.Fatalf("write cast: %v", err)
	}
	addPtyRow(t, s, "job-rec", "alice", castPath, 2)

	resp := getRecording(t, s, "job-rec", "tok-alice")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-asciicast" {
		t.Fatalf("content-type=%q, want application/x-asciicast", ct)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("body=%q, want %q", got, body)
	}
}

func TestPtyRecordingAdminOtherOwner(t *testing.T) {
	s := recordingServer(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "admin", Token: "tok-admin", CanAdmin: true},
		},
	})
	dir := addRecordingJob(t, s, "job-a", "alice", "")
	castPath := filepath.Join(dir, "pty.cast")
	if err := os.WriteFile(castPath, []byte("cast"), 0o600); err != nil {
		t.Fatalf("write cast: %v", err)
	}
	addPtyRow(t, s, "job-a", "alice", castPath, 2)

	resp := getRecording(t, s, "job-a", "tok-admin")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPtyRecordingRejectsOtherCaller(t *testing.T) {
	s := recordingServer(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
		},
	})
	dir := addRecordingJob(t, s, "job-a", "alice", "")
	castPath := filepath.Join(dir, "pty.cast")
	_ = os.WriteFile(castPath, []byte("cast"), 0o600)
	addPtyRow(t, s, "job-a", "alice", castPath, 2)

	resp := getRecording(t, s, "job-a", "tok-bob")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPtyRecordingEmptyOwnerRequiresAdmin(t *testing.T) {
	// allow_empty pass-through (caller=""): an unowned job's recording is
	// admin-only, and an empty caller is never admin → 403.
	s := recordingServer(t, config.ServerConfig{AllowEmptyToken: true})
	dir := addRecordingJob(t, s, "job-legacy", "", "")
	castPath := filepath.Join(dir, "pty.cast")
	_ = os.WriteFile(castPath, []byte("cast"), 0o600)
	addPtyRow(t, s, "job-legacy", "", castPath, 2)

	resp := getRecording(t, s, "job-legacy", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestPtyRecordingWorkerSourceStillDownloadable proves a worker-routed job's cast
// is served from the hub (200), NOT 409'd on job.Source: the pty cast is always
// written hub-side, so a remote command Source must not gate the download.
func TestPtyRecordingWorkerSourceStillDownloadable(t *testing.T) {
	s := recordingServer(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	dir := addRecordingJob(t, s, "job-remote", "alice", "worker:w1")
	castPath := filepath.Join(dir, "pty.cast")
	_ = os.WriteFile(castPath, []byte("cast"), 0o600)
	addPtyRow(t, s, "job-remote", "alice", castPath, 2)

	resp := getRecording(t, s, "job-remote", "tok-alice")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, []byte("cast")) {
		t.Fatalf("body=%q, want %q", got, "cast")
	}
}

func TestPtyRecordingUnknownJob404(t *testing.T) {
	s := recordingServer(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	resp := getRecording(t, s, "missing", "tok-alice")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPtyRecordingNoSessionRow404(t *testing.T) {
	s := recordingServer(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	addRecordingJob(t, s, "job-norow", "alice", "") // job but no pty_sessions row
	resp := getRecording(t, s, "job-norow", "tok-alice")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPtyRecordingEmptyURI404(t *testing.T) {
	// TTL-expired-cleared row: RecordingURI blanked → 404.
	s := recordingServer(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	addRecordingJob(t, s, "job-expired", "alice", "")
	addPtyRow(t, s, "job-expired", "alice", "", 2)
	resp := getRecording(t, s, "job-expired", "tok-alice")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPtyRecordingFileGone404(t *testing.T) {
	s := recordingServer(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	dir := addRecordingJob(t, s, "job-gone", "alice", "")
	// Row points at a cast file that was never written.
	addPtyRow(t, s, "job-gone", "alice", filepath.Join(dir, "pty.cast"), 2)
	resp := getRecording(t, s, "job-gone", "tok-alice")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPtyRecordingEncryptedDecrypts(t *testing.T) {
	s := recordingServer(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	dir := addRecordingJob(t, s, "job-enc", "alice", "")
	castPath := filepath.Join(dir, "pty.cast")
	rec := writeEncryptedCast(t, castPath, "hello-encrypted-cast")
	s.SetCastRecorder(rec)
	addPtyRow(t, s, "job-enc", "alice", castPath, 1)

	resp := getRecording(t, s, "job-enc", "tok-alice")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-asciicast" {
		t.Fatalf("content-type=%q, want application/x-asciicast", ct)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Independent decrypt of the same file must equal the streamed body (proves the
	// handler returned the full decrypted plaintext).
	dr, err := rec.NewDecReader(castPath)
	if err != nil {
		t.Fatalf("independent dec reader: %v", err)
	}
	defer dr.Close()
	want, _ := io.ReadAll(dr)
	if !bytes.Equal(got, want) {
		t.Fatalf("decrypted body mismatch:\n got=%q\nwant=%q", got, want)
	}
	if !bytes.Contains(got, []byte("hello-encrypted-cast")) {
		t.Fatalf("decrypted body missing payload: %q", got)
	}
}

func TestPtyRecordingEncryptedHeaderTamper4xxNoPartial(t *testing.T) {
	s := recordingServer(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	dir := addRecordingJob(t, s, "job-badhdr", "alice", "")
	castPath := filepath.Join(dir, "pty.cast")
	rec := writeEncryptedCast(t, castPath, "content")
	s.SetCastRecorder(rec)
	// Corrupt the file magic (header authentication fails in NewDecReader, before
	// any 200 is written).
	data, _ := os.ReadFile(castPath)
	data[0] ^= 0xff
	if err := os.WriteFile(castPath, data, 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	addPtyRow(t, s, "job-badhdr", "alice", castPath, 1)

	resp := getRecording(t, s, "job-badhdr", "tok-alice")
	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Fatalf("status=%d, want 4xx (no half-sent 200)", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPtyRecordingEncryptedFirstFrameTamper4xx(t *testing.T) {
	s := recordingServer(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	dir := addRecordingJob(t, s, "job-badframe", "alice", "")
	castPath := filepath.Join(dir, "pty.cast")
	rec := writeEncryptedCast(t, castPath, "content-frame")
	s.SetCastRecorder(rec)
	// Corrupt a byte inside the first frame ciphertext (header is 21 bytes, then a
	// 4-byte length prefix): the header authenticates but the first-frame GCM tag
	// fails during the eager first Read → 4xx before a 200 is committed.
	data, _ := os.ReadFile(castPath)
	data[30] ^= 0xff
	if err := os.WriteFile(castPath, data, 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	addPtyRow(t, s, "job-badframe", "alice", castPath, 1)

	resp := getRecording(t, s, "job-badframe", "tok-alice")
	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Fatalf("status=%d, want 4xx", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPtyRecordingEmitsAuditLog(t *testing.T) {
	prev := slog.Default()
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s := recordingServer(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	dir := addRecordingJob(t, s, "job-audit", "alice", "")
	castPath := filepath.Join(dir, "pty.cast")
	_ = os.WriteFile(castPath, []byte("cast"), 0o600)
	addPtyRow(t, s, "job-audit", "alice", castPath, 2)

	resp := getRecording(t, s, "job-audit", "tok-alice")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	if !bytes.Contains(buf.Bytes(), []byte("pty recording download")) ||
		!bytes.Contains(buf.Bytes(), []byte("job-audit")) {
		t.Fatalf("audit log not emitted: %s", buf.String())
	}
}
