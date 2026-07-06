//go:build unix

// pty_cast_e2e_test.go is the WEB-03 P3 T7 end-to-end matrix for cast recording:
// it reuses the P2 full-stack pty attach harness (real httptest serve core +
// worker.Client + REAL pty child + browser attach — see pty_e2e_test.go) and adds
// a wired cast Recorder + pty-session store on the serve side, so these tests
// prove the recording round-trip END TO END: a real interactive session produces
// a real pty.cast under the host result dir, the pty_sessions row is finalised
// with byte counts, and GET /v1/jobs/{id}/pty/recording streams it back — both
// plaintext and encrypted. It also covers the two retention regimes (cast TTL
// sweep keeps the row / clears the recording; job prune deletes the row + dir) and
// that the recording download does NOT gate on job.Source (design §4/§5, P3-plan T7).
//
// Empirical grounding (verified with throwaway probes on the container's real pty
// before these tests were written):
//   - a CANCELLED interactive session finalises with an EMPTY job Source
//     (the worker runner's ctx.Done path returns no Outcome).
//   - a NATURALLY-completed worker job finalises with Source="worker:<id>", but the
//     pty.cast physically lives on the HUB — so the owner can still download it (200),
//     because the recording gate is source-independent (see
//     TestE2EPtyCastRecordingWorkerSourceStillDownloadable).
package worker_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/castrec"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// buildCastHubSide mirrors buildInteractiveHubSide (pty_e2e_test.go) but wires the
// WEB-03 P3 cast recording factory + pty-session persistence onto the serve side.
// castCfg drives the Recorder (secret is only consulted when encryption is on);
// retention drives Service.Prune (regime tests). It returns the same *ptyHubSide
// the P2 helpers consume plus the live jobstore (for direct pty_sessions seeding /
// reads). The single Recorder instance is shared by handlePtyConnect (record) and
// handlePtyRecording (decrypt) — exactly as serve wires it in production (T4).
func buildCastHubSide(t *testing.T, castCfg config.CastConfig, retention config.RetentionConfig, secret []byte) (*ptyHubSide, *jobstore.Store) {
	t.Helper()
	host := t.TempDir()
	root := t.TempDir()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Token:   "server-default-token",
			Workers: map[string]config.WorkerAuthConfig{e2eWorkerID: {Token: e2eToken}},
		},
		Storage:  config.StorageConfig{Root: root, Cast: castCfg, Retention: retention},
		Projects: map[string]config.ProjectConfig{"alpha": interactiveProject(host, []string{"remote-w1"}, 0)},
		Agents:   interactiveAgents(),
		Runners:  map[string]config.RunnerConfig{"remote-w1": {Type: "worker", WorkerID: e2eWorkerID}},
	}
	config.ApplyDefaults(cfg)

	cr, err := core.Build(cfg)
	if err != nil {
		t.Fatalf("core.Build: %v", err)
	}
	t.Cleanup(func() { _ = cr.Close() })

	srv := httpapi.New(&cfg.Server, "server-default-token", false, cr.Jobs, cr.Workflow(), cr.Projects, cr.Agents, cr.Hub, cfg.Runners, nil, nil)
	srv.SetPtyRelay(cr.RelayNonces, cr.PtyRelays)
	store := cr.Jobs.Meta()
	srv.SetPtySessionStore(store)
	if castCfg.Enabled {
		rec, err := castrec.New(castCfg, secret)
		if err != nil {
			t.Fatalf("castrec.New: %v", err)
		}
		srv.SetCastRecorder(rec)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &ptyHubSide{ts: ts, jobs: cr.Jobs, relays: cr.PtyRelays, hub: cr.Hub}, store
}

// waitClosedPtySession polls the store until jobID's pty session reaches state
// "closed" (the handler writes the closed row in its finalize defer, which runs
// after the host job converges — so a poll avoids racing that handoff).
func waitClosedPtySession(t *testing.T, store *jobstore.Store, jobID string) jobstore.PtySessionRecord {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rec, ok, err := store.GetPtySessionByJob(jobID)
		if err != nil {
			t.Fatalf("GetPtySessionByJob: %v", err)
		}
		if ok && rec.State == "closed" {
			return rec
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec, ok, _ := store.GetPtySessionByJob(jobID)
	t.Fatalf("pty session for %s never reached closed; last=%+v ok=%v", jobID, rec, ok)
	return jobstore.PtySessionRecord{}
}

// getRecording GETs /v1/jobs/{id}/pty/recording as the default caller (which owns
// jobs it created in this harness). The caller must Close the body.
func getRecording(t *testing.T, hub *ptyHubSide, jobID string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, hub.ts.URL+"/v1/jobs/"+jobID+"/pty/recording", nil)
	req.Header.Set("Authorization", "Bearer server-default-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET pty/recording: %v", err)
	}
	return resp
}

// --- recording round-trip (plaintext) --------------------------------------

func createRecordedInteractiveJob(t *testing.T, hub *ptyHubSide, agentName string) string {
	t.Helper()
	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: agentName, Runner: "remote-w1", WorkerID: e2eWorkerID,
		Prompt: "start", Cwd: ".", TimeoutSec: 60, Interactive: true, RecordPty: true,
	})
	if created.ID == "" {
		t.Fatal("recorded interactive job has no id")
	}
	return created.ID
}

// TestE2EPtyCastRecordingRoundTripPlaintext proves the full plaintext recording
// path end to end: a real interactive termecho session records its output to a
// pty.cast, the pty_sessions row is finalised (open→closed) with real byte counts,
// and the owner downloads the asciinema v2 recording containing the echoed output.
// The session is ended by CANCEL so the host job's Source stays empty (the gate
// therefore does not 409 — see file header).
func TestE2EPtyCastRecordingRoundTripPlaintext(t *testing.T) {
	hub, store := buildCastHubSide(t,
		config.CastConfig{Enabled: true, RetentionTTLHours: 24}, config.RetentionConfig{}, nil)
	cl, _ := buildInteractiveWorkerSide(t, hub.ts.URL, 0)
	startWorker(t, hub, cl)

	jobID := createRecordedInteractiveJob(t, hub, "termecho")
	conn := attachBrowser(t, hub, jobID)

	// The echoed output flows through the relay → the cast sink, so the recording
	// contains it.
	sendBrowserInput(t, conn, []byte("roundtrip-plain\n"))
	readBrowserUntil(t, conn, "echo:roundtrip-plain", 5*time.Second)

	cancelHostJob(t, hub, jobID)
	final := waitHostTerminal(t, hub, jobID)
	if job.IsRemoteSource(final.Source) {
		t.Fatalf("cancelled interactive job has remote Source %q (expected empty)", final.Source)
	}

	closed := waitClosedPtySession(t, store, jobID)
	if closed.RecordingURI == "" || closed.Encrypted != 2 {
		t.Fatalf("closed row = %+v, want recording_uri set + encrypted=2", closed)
	}
	if closed.BytesIn <= 0 || closed.BytesOut <= 0 {
		t.Fatalf("byte counts not recorded: in=%d out=%d", closed.BytesIn, closed.BytesOut)
	}
	if closed.StartedAt <= 0 || closed.EndedAt < closed.StartedAt {
		t.Fatalf("timestamps unreasonable: started=%d ended=%d", closed.StartedAt, closed.EndedAt)
	}
	// The real pty.cast exists under the host job's result dir.
	if data, err := os.ReadFile(filepath.Join(final.ResultDir, "pty.cast")); err != nil || len(data) == 0 {
		t.Fatalf("pty.cast on disk = %d bytes, err %v", len(data), err)
	}

	resp := getRecording(t, hub, jobID)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET recording status=%d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-asciicast" {
		t.Fatalf("content-type=%q, want application/x-asciicast", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte(`"version":2`)) {
		t.Fatalf("recording missing asciinema v2 header: %q", body[:min(len(body), 120)])
	}
	if !bytes.Contains(body, []byte("echo:roundtrip-plain")) {
		t.Fatalf("recording missing echoed output; got %q", body)
	}
}

// --- recording round-trip (encrypted) --------------------------------------

// TestE2EPtyCastRecordingRoundTripEncrypted proves the encrypted path end to end:
// the on-disk pty.cast is framed AEAD (GFC1 magic, plaintext never leaks) and the
// download stream-decrypts through the SAME recorder to the plaintext asciinema,
// containing the echoed output.
func TestE2EPtyCastRecordingRoundTripEncrypted(t *testing.T) {
	secret := bytes.Repeat([]byte{0x5A}, 32)
	hub, store := buildCastHubSide(t,
		config.CastConfig{
			Enabled:           true,
			RetentionTTLHours: 24,
			Encryption:        config.CastEncryptionConfig{Enabled: true, KeyEnv: "GOFER_TEST_CAST_KEY"},
		}, config.RetentionConfig{}, secret)
	cl, _ := buildInteractiveWorkerSide(t, hub.ts.URL, 0)
	startWorker(t, hub, cl)

	jobID := createRecordedInteractiveJob(t, hub, "termecho")
	conn := attachBrowser(t, hub, jobID)
	sendBrowserInput(t, conn, []byte("roundtrip-enc\n"))
	readBrowserUntil(t, conn, "echo:roundtrip-enc", 5*time.Second)

	cancelHostJob(t, hub, jobID)
	final := waitHostTerminal(t, hub, jobID)
	if job.IsRemoteSource(final.Source) {
		t.Fatalf("cancelled interactive job has remote Source %q", final.Source)
	}

	closed := waitClosedPtySession(t, store, jobID)
	if closed.Encrypted != 1 || closed.RecordingURI == "" {
		t.Fatalf("closed row = %+v, want encrypted=1 + recording_uri set", closed)
	}
	// On-disk file is encrypted: framed AEAD magic, no plaintext leak.
	raw, err := os.ReadFile(filepath.Join(final.ResultDir, "pty.cast"))
	if err != nil || len(raw) < 4 {
		t.Fatalf("encrypted pty.cast = %d bytes, err %v", len(raw), err)
	}
	if string(raw[:4]) != "GFC1" {
		t.Fatalf("encrypted cast magic = %q, want GFC1", raw[:4])
	}
	if bytes.Contains(raw, []byte("echo:roundtrip-enc")) {
		t.Fatal("plaintext leaked into the encrypted cast file")
	}

	resp := getRecording(t, hub, jobID)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET encrypted recording status=%d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-asciicast" {
		t.Fatalf("content-type=%q, want application/x-asciicast", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte(`"version":2`)) || !bytes.Contains(body, []byte("echo:roundtrip-enc")) {
		t.Fatalf("decrypted recording missing header/output; got %q", body[:min(len(body), 160)])
	}
}

// --- worker-source recording is still downloadable (source-independent gate) --

// TestE2EPtyCastRecordingWorkerSourceStillDownloadable proves that a
// NATURALLY-completed worker-routed interactive job — which finalises with
// Source="worker:<id>" — has its pty recording served (200) from the hub: the
// pty.cast is produced hub-side by the relay regardless of where the job command
// ran, so the recording download must NOT gate on job.Source (D-P3-7 fix). The
// owner GETs the recording and reads back the recorded output.
func TestE2EPtyCastRecordingWorkerSourceStillDownloadable(t *testing.T) {
	hub, store := buildCastHubSide(t,
		config.CastConfig{Enabled: true, RetentionTTLHours: 24}, config.RetentionConfig{}, nil)
	cl, _ := buildInteractiveWorkerSide(t, hub.ts.URL, 0)
	startWorker(t, hub, cl)

	jobID := createRecordedInteractiveJob(t, hub, "termtail")
	conn := attachBrowser(t, hub, jobID)
	sendBrowserInput(t, conn, []byte("go\n"))
	readBrowserUntil(t, conn, "FINAL_TAIL_SENTINEL_9Z", 8*time.Second)

	final := waitHostTerminal(t, hub, jobID)
	if final.Status != job.StatusDone {
		t.Fatalf("status=%s, want done (natural exit)", final.Status)
	}
	// The job is worker-routed (remote Source), yet the cast lives on the hub.
	if !job.IsRemoteSource(final.Source) {
		t.Fatalf("natural-exit worker job Source=%q, expected remote (worker:...)", final.Source)
	}

	// The recording was really produced on the hub.
	closed := waitClosedPtySession(t, store, jobID)
	if closed.RecordingURI == "" {
		t.Fatalf("closed row has no recording_uri: %+v", closed)
	}
	if _, err := os.Stat(filepath.Join(final.ResultDir, "pty.cast")); err != nil {
		t.Fatalf("pty.cast should exist on the hub: %v", err)
	}

	// Source-independent gate: worker-routed job → 200 + downloadable cast content.
	resp := getRecording(t, hub, jobID)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET recording status=%d, want 200 (source-independent gate)", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-asciicast" {
		t.Fatalf("content-type=%q, want application/x-asciicast", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte(`"version":2`)) {
		t.Fatalf("recording missing asciinema v2 header: %q", body[:min(len(body), 120)])
	}
	if !bytes.Contains(body, []byte("FINAL_TAIL_SENTINEL_9Z")) {
		t.Fatalf("recording missing recorded output; got %q", body)
	}
}

// --- retention regime 1: cast TTL sweep keeps the row, clears the recording --

// TestE2EPtyCastRetentionRegime1 exercises Service.Prune's cast sweep end to end:
// a closed pty session whose cast TTL has elapsed has its pty.cast file deleted on
// disk and its recording_uri cleared, while the session ROW is retained for audit;
// the download gate then returns 404 (recording expired). Retention (job/wf) is
// disabled so only the cast sweep runs.
func TestE2EPtyCastRetentionRegime1(t *testing.T) {
	hub, store := buildCastHubSide(t,
		config.CastConfig{Enabled: true, RetentionTTLHours: 1}, config.RetentionConfig{}, nil)

	// Seed a fresh interactive job (owned by the default caller, local source) with
	// a real pty.cast and a closed session row whose ended_at is ancient → expired.
	dir := t.TempDir()
	castPath := filepath.Join(dir, "pty.cast")
	if err := os.WriteFile(castPath, []byte("{\"version\":2}\n[0.1,\"o\",\"x\"]\n"), 0o600); err != nil {
		t.Fatalf("write cast: %v", err)
	}
	now := time.Now().Unix()
	if err := store.UpsertJob(jobstore.JobRecord{
		ID: "job-r1", ProjectKey: "alpha", Agent: "termecho", Runner: "remote-w1", Interactive: true,
		Status: "done", Cwd: ".", ResultDir: dir, CallerID: "default",
		StartedAt: now, EndedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert job: %v", err)
	}
	if err := store.UpsertPtySession(jobstore.PtySessionRecord{
		PtySessionID: "ps-r1", JobID: "job-r1", Owner: "default", State: "closed",
		RecordingURI: castPath, Encrypted: 2, BytesOut: 24,
		StartedAt: 1000, EndedAt: 2000, // ancient → older than TTL vs real now
	}); err != nil {
		t.Fatalf("upsert pty session: %v", err)
	}

	if _, err := hub.jobs.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	// cast file deleted.
	if _, err := os.Stat(castPath); !os.IsNotExist(err) {
		t.Fatalf("pty.cast should be deleted by the cast sweep; stat err=%v", err)
	}
	// row retained, recording_uri cleared, encrypted reset to 2.
	got, ok, err := store.GetPtySessionByJob("job-r1")
	if err != nil || !ok {
		t.Fatalf("session row should be retained after cast sweep: ok=%v err=%v", ok, err)
	}
	if got.RecordingURI != "" || got.Encrypted != 2 {
		t.Fatalf("row = %+v, want recording_uri empty + encrypted=2", got)
	}
	// gate 404 (recording expired / cleared).
	resp := getRecording(t, hub, "job-r1")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET recording after sweep status=%d, want 404", resp.StatusCode)
	}
}

// --- retention regime 2: job prune deletes the row + result dir -------------

// TestE2EPtyCastRetentionRegime2 exercises Service.Prune's job retention end to
// end: an aged terminal interactive job is pruned, deleting its pty_sessions row
// (same transaction) and its result dir — which carries the pty.cast — off disk.
func TestE2EPtyCastRetentionRegime2(t *testing.T) {
	hub, store := buildCastHubSide(t,
		config.CastConfig{}, config.RetentionConfig{MaxAgeDays: 1}, nil)

	// Seed an ancient terminal interactive job with a result dir containing a
	// pty.cast + a closed session row.
	dir := t.TempDir()
	castPath := filepath.Join(dir, "pty.cast")
	if err := os.WriteFile(castPath, []byte("recorded-cast-bytes"), 0o600); err != nil {
		t.Fatalf("write cast: %v", err)
	}
	old := int64(1000) // ~1970 → far older than MaxAgeDays=1
	if err := store.UpsertJob(jobstore.JobRecord{
		ID: "job-r2", ProjectKey: "alpha", Agent: "termecho", Runner: "remote-w1", Interactive: true,
		Status: "done", Cwd: ".", ResultDir: dir, CallerID: "default",
		StartedAt: old, EndedAt: old, UpdatedAt: old,
	}); err != nil {
		t.Fatalf("upsert job: %v", err)
	}
	if err := store.UpsertPtySession(jobstore.PtySessionRecord{
		PtySessionID: "ps-r2", JobID: "job-r2", Owner: "default", State: "closed",
		RecordingURI: castPath, Encrypted: 2, BytesOut: 19, StartedAt: old, EndedAt: old,
	}); err != nil {
		t.Fatalf("upsert pty session: %v", err)
	}

	deleted, err := hub.jobs.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("pruned %d jobs, want 1", deleted)
	}

	// session row gone with the job (same tx).
	if _, ok, err := store.GetPtySessionByJob("job-r2"); err != nil || ok {
		t.Fatalf("pty session should be pruned with the job: ok=%v err=%v", ok, err)
	}
	// result dir (incl pty.cast) removed off disk.
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("result dir should be removed by job prune; stat err=%v", err)
	}
}
