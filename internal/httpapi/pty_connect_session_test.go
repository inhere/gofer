package httpapi

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/castrec"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/ptyrelay"
)

// waitPtySession polls the store for a job's pty session in the wanted state
// (the open/closed rows are written by the handler asynchronously to the relay
// state transitions, so a poll avoids racing them).
func waitPtySession(t *testing.T, store PtySessionStore, jobID, state string) jobstore.PtySessionRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec, ok, err := store.GetPtySessionByJob(jobID)
		if err != nil {
			t.Fatalf("get pty session: %v", err)
		}
		if ok && rec.State == state {
			return rec
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec, ok, _ := store.GetPtySessionByJob(jobID)
	t.Fatalf("pty session for %s not in state %q; last=%+v ok=%v", jobID, state, rec, ok)
	return jobstore.PtySessionRecord{}
}

// driveAndCloseRelay writes worker output, forwards one stdin chunk through a
// write-lease viewer, then closes the pty ws so the handler's finalize path runs.
// It returns the job's result dir (for cast-file assertions).
func driveAndCloseRelay(t *testing.T, s *Server, base, jobID, sessionID, nonce string, conn *websocket.Conn) string {
	t.Helper()
	res, ok := s.jobs.Get(jobID)
	if !ok {
		t.Fatalf("job %s missing", jobID)
	}
	entry := waitForPtyRelay(t, s.ptyRelays, jobID, ptyrelay.RelayOpen)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("worker-out")); err != nil {
		t.Fatalf("write worker binary: %v", err)
	}
	waitRecordedLen(t, entry.Relay, len("worker-out"))

	viewer, err := entry.Relay.AddViewer(true)
	if err != nil {
		t.Fatalf("add writer viewer: %v", err)
	}
	if err := viewer.SendInput([]byte("stdin")); err != nil {
		t.Fatalf("send input: %v", err)
	}
	// Drain the echoed input frame so the ws write side never backpressures.
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("read echoed input frame: %v", err)
	}

	// Closing the client unblocks the source Read → recordLoop exits → finish →
	// the handler's select returns and its finalize defer writes the closed row.
	_ = conn.Close(websocket.StatusNormalClosure, "test done")
	return res.ResultDir
}

// TestPtyConnectRecordsSessionPlaintext: recording enabled (plaintext) →
// pty.cast is written and the pty_sessions row goes open→closed with bytes
// counted and recording_uri set (encrypted=2).
func TestPtyConnectRecordsSessionPlaintext(t *testing.T) {
	s, nonces, relays, base, _ := newPtyConnectTestServer(t)
	store := s.jobs.Meta()
	s.SetPtySessionStore(store)
	rec, err := castrec.New(config.CastConfig{Enabled: true}, nil)
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	s.SetCastRecorder(rec)

	nonce := preparePtyRelay(t, s, nonces, relays, "job-rec", "pty-rec", ptyTestInst)
	conn := dialPtyAndHello(t, base, ptyConnectHello{JobID: "job-rec", PtySessionID: "pty-rec", RelayNonce: nonce})

	// open row visible with a recording_uri before close.
	openRow := waitPtySession(t, store, "job-rec", "open")
	if openRow.RecordingURI == "" || openRow.Encrypted != 2 || openRow.Owner != "" {
		t.Fatalf("open row = %+v, want recording_uri set, encrypted=2", openRow)
	}

	resultDir := driveAndCloseRelay(t, s, base, "job-rec", "pty-rec", nonce, conn)

	closed := waitPtySession(t, store, "job-rec", "closed")
	if closed.EndedAt == 0 {
		t.Fatalf("closed row ended_at=0: %+v", closed)
	}
	if closed.BytesOut < int64(len("worker-out")) {
		t.Fatalf("bytes_out=%d, want >= %d", closed.BytesOut, len("worker-out"))
	}
	if closed.BytesIn != int64(len("stdin")) {
		t.Fatalf("bytes_in=%d, want %d", closed.BytesIn, len("stdin"))
	}
	if closed.RecordingURI == "" || closed.Encrypted != 2 {
		t.Fatalf("closed row = %+v, want recording_uri set, encrypted=2", closed)
	}
	data, rerr := os.ReadFile(filepath.Join(resultDir, "pty.cast"))
	if rerr != nil || len(data) == 0 {
		t.Fatalf("pty.cast read = %d bytes, err %v", len(data), rerr)
	}
	if !bytes.Contains(data, []byte(`"version":2`)) {
		t.Fatalf("plaintext cast missing asciinema v2 header: %s", data[:min(len(data), 120)])
	}
}

// TestPtyConnectRecordsSessionNoRecorder: store wired but recorder nil → the
// session row is still recorded (recording independent of the sink), with an
// empty recording_uri and no cast file (G023 default path).
func TestPtyConnectRecordsSessionNoRecorder(t *testing.T) {
	s, nonces, relays, base, _ := newPtyConnectTestServer(t)
	store := s.jobs.Meta()
	s.SetPtySessionStore(store) // no SetCastRecorder → castRecorder nil

	nonce := preparePtyRelay(t, s, nonces, relays, "job-norec", "pty-norec", ptyTestInst)
	conn := dialPtyAndHello(t, base, ptyConnectHello{JobID: "job-norec", PtySessionID: "pty-norec", RelayNonce: nonce})
	resultDir := driveAndCloseRelay(t, s, base, "job-norec", "pty-norec", nonce, conn)

	closed := waitPtySession(t, store, "job-norec", "closed")
	if closed.RecordingURI != "" {
		t.Fatalf("recording_uri=%q, want empty (no recorder)", closed.RecordingURI)
	}
	if closed.Encrypted != 2 {
		t.Fatalf("encrypted=%d, want 2", closed.Encrypted)
	}
	if closed.BytesOut < int64(len("worker-out")) || closed.BytesIn != int64(len("stdin")) {
		t.Fatalf("bytes = out %d / in %d", closed.BytesOut, closed.BytesIn)
	}
	if _, err := os.Stat(filepath.Join(resultDir, "pty.cast")); !os.IsNotExist(err) {
		t.Fatalf("pty.cast should not exist without a recorder, stat err=%v", err)
	}
}

func TestPtyConnectRecorderWiredButNotRequested(t *testing.T) {
	s, nonces, relays, base, _ := newPtyConnectTestServer(t)
	store := s.jobs.Meta()
	s.SetPtySessionStore(store)
	rec, err := castrec.New(config.CastConfig{Enabled: true}, nil)
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	s.SetCastRecorder(rec)

	now := time.Now().Unix()
	resultDir := t.TempDir()
	if err := store.UpsertJob(jobstore.JobRecord{
		ID: "job-optout", ProjectKey: "self", Agent: "exec", Runner: "worker", Interactive: true,
		Status: "running", Cwd: ".", ResultDir: resultDir,
		RequestJSON: `{"project_key":"self","agent":"exec","runner":"worker","interactive":true}`,
		StartedAt:   now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert job: %v", err)
	}
	expiry := time.Now().Add(time.Minute).Unix()
	nonce := nonces.Issue(ptyrelay.NonceBinding{
		WorkerID: ptyTestWorkerID, InstanceID: ptyTestInst, JobID: "job-optout", PtySessionID: "pty-optout", Expiry: expiry,
	})
	relays.Prepare(ptyrelay.RelayBinding{
		WorkerID: ptyTestWorkerID, InstanceID: ptyTestInst, JobID: "job-optout", PtySessionID: "pty-optout", Nonce: nonce, Expiry: expiry,
	})
	conn := dialPtyAndHello(t, base, ptyConnectHello{JobID: "job-optout", PtySessionID: "pty-optout", RelayNonce: nonce})
	_ = driveAndCloseRelay(t, s, base, "job-optout", "pty-optout", nonce, conn)

	closed := waitPtySession(t, store, "job-optout", "closed")
	if closed.RecordingURI != "" {
		t.Fatalf("recording_uri=%q, want empty when record_pty is not requested", closed.RecordingURI)
	}
	if _, err := os.Stat(filepath.Join(resultDir, "pty.cast")); !os.IsNotExist(err) {
		t.Fatalf("pty.cast should not exist without record_pty, stat err=%v", err)
	}
}

// TestPtyConnectRecordsSessionOpenFailureDegrades: the recorder is wired but the
// cast file cannot be created (result dir does not exist) → the handler degrades
// to "not recording" (empty recording_uri) yet STILL records the session row.
func TestPtyConnectRecordsSessionOpenFailureDegrades(t *testing.T) {
	s, nonces, relays, base, _ := newPtyConnectTestServer(t)
	store := s.jobs.Meta()
	s.SetPtySessionStore(store)
	rec, err := castrec.New(config.CastConfig{Enabled: true}, nil)
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	s.SetCastRecorder(rec)

	now := time.Now().Unix()
	badDir := filepath.Join(t.TempDir(), "missing-subdir") // never created → Open fails
	if err := store.UpsertJob(jobstore.JobRecord{
		ID: "job-badrec", ProjectKey: "self", Agent: "exec", Runner: "worker", Interactive: true,
		Status: "running", Cwd: ".", ResultDir: badDir, StartedAt: now, UpdatedAt: now,
		RequestJSON: `{"project_key":"self","agent":"exec","runner":"worker","interactive":true,"record_pty":true}`,
	}); err != nil {
		t.Fatalf("upsert job: %v", err)
	}
	expiry := time.Now().Add(time.Minute).Unix()
	nonce := nonces.Issue(ptyrelay.NonceBinding{
		WorkerID: ptyTestWorkerID, InstanceID: ptyTestInst, JobID: "job-badrec", PtySessionID: "pty-badrec", Expiry: expiry,
	})
	relays.Prepare(ptyrelay.RelayBinding{
		WorkerID: ptyTestWorkerID, InstanceID: ptyTestInst, JobID: "job-badrec", PtySessionID: "pty-badrec", Nonce: nonce, Expiry: expiry,
	})
	conn := dialPtyAndHello(t, base, ptyConnectHello{JobID: "job-badrec", PtySessionID: "pty-badrec", RelayNonce: nonce})
	_ = driveAndCloseRelay(t, s, base, "job-badrec", "pty-badrec", nonce, conn)

	closed := waitPtySession(t, store, "job-badrec", "closed")
	if closed.RecordingURI != "" {
		t.Fatalf("recording_uri=%q, want empty after Open failure", closed.RecordingURI)
	}
	if closed.BytesOut < int64(len("worker-out")) {
		t.Fatalf("bytes_out=%d, want >= %d", closed.BytesOut, len("worker-out"))
	}
}

// TestPtyConnectRecordsSessionEncrypted: encrypted recorder → the row is
// encrypted=1 and pty.cast starts with the framed AEAD magic (GFC1).
func TestPtyConnectRecordsSessionEncrypted(t *testing.T) {
	s, nonces, relays, base, _ := newPtyConnectTestServer(t)
	store := s.jobs.Meta()
	s.SetPtySessionStore(store)
	rec, err := castrec.New(config.CastConfig{
		Enabled:    true,
		Encryption: config.CastEncryptionConfig{Enabled: true},
	}, bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatalf("new encrypted recorder: %v", err)
	}
	s.SetCastRecorder(rec)

	nonce := preparePtyRelay(t, s, nonces, relays, "job-enc", "pty-enc", ptyTestInst)
	conn := dialPtyAndHello(t, base, ptyConnectHello{JobID: "job-enc", PtySessionID: "pty-enc", RelayNonce: nonce})
	resultDir := driveAndCloseRelay(t, s, base, "job-enc", "pty-enc", nonce, conn)

	closed := waitPtySession(t, store, "job-enc", "closed")
	if closed.Encrypted != 1 || closed.RecordingURI == "" {
		t.Fatalf("closed row = %+v, want encrypted=1 recording_uri set", closed)
	}
	data, rerr := os.ReadFile(filepath.Join(resultDir, "pty.cast"))
	if rerr != nil || len(data) < 4 {
		t.Fatalf("pty.cast read = %d bytes, err %v", len(data), rerr)
	}
	if string(data[:4]) != "GFC1" {
		t.Fatalf("encrypted cast magic = %q, want GFC1", data[:4])
	}
}

// TestPtyConnectNoDialNoSession (H4): a prepared-but-never-dialled relay records
// no pty_sessions row — the handler never runs, so nothing is persisted.
func TestPtyConnectNoDialNoSession(t *testing.T) {
	s, nonces, relays, _, _ := newPtyConnectTestServer(t)
	store := s.jobs.Meta()
	s.SetPtySessionStore(store)
	_ = preparePtyRelay(t, s, nonces, relays, "job-nodial", "pty-nodial", ptyTestInst)

	if _, ok, err := store.GetPtySessionByJob("job-nodial"); err != nil || ok {
		t.Fatalf("GetPtySessionByJob(no-dial) = ok %v err %v, want no row", ok, err)
	}
}
