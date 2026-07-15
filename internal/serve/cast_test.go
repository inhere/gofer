package serve

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/jobstore"
)

// buildCastRecorder returns a nil recorder when recording is disabled (G023 default).
func TestBuildCastRecorderDisabled(t *testing.T) {
	cfg := &config.Config{}
	rec, err := buildCastRecorder(cfg)
	if err != nil {
		t.Fatalf("buildCastRecorder: %v", err)
	}
	if rec != nil {
		t.Fatal("recorder should be nil when cast recording is disabled")
	}
}

// Enabled + plaintext (encryption off) builds a non-nil, non-encrypting recorder
// without touching any env var.
func TestBuildCastRecorderEnabledPlaintext(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.Cast = config.CastConfig{Enabled: true, RetentionTTLHours: 24}
	rec, err := buildCastRecorder(cfg)
	if err != nil {
		t.Fatalf("buildCastRecorder plaintext: %v", err)
	}
	if rec == nil {
		t.Fatal("recorder should be non-nil when cast recording is enabled")
	}
	if rec.Encrypted() {
		t.Fatal("plaintext recorder must not report encrypted")
	}
}

// Enabled + encryption + a valid >=32B base64 key builds an encrypting recorder.
func TestBuildCastRecorderEnabledEncrypted(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	t.Setenv("GOFER_TEST_SERVE_CAST_KEY", base64.StdEncoding.EncodeToString(key))
	cfg := &config.Config{}
	cfg.Storage.Cast = config.CastConfig{
		Enabled: true, RetentionTTLHours: 48,
		Encryption: config.CastEncryptionConfig{Enabled: true, KeyEnv: "GOFER_TEST_SERVE_CAST_KEY"},
	}
	rec, err := buildCastRecorder(cfg)
	if err != nil {
		t.Fatalf("buildCastRecorder encrypted: %v", err)
	}
	if rec == nil {
		t.Fatal("recorder should be non-nil")
	}
	if !rec.Encrypted() {
		t.Fatal("encrypted recorder must report encrypted")
	}
}

// Enabled + encryption + a missing key fails serve assembly fast.
func TestBuildCastRecorderEncryptedMissingKey(t *testing.T) {
	t.Setenv("GOFER_TEST_SERVE_CAST_KEY", "") // unset
	cfg := &config.Config{}
	cfg.Storage.Cast = config.CastConfig{
		Enabled: true, RetentionTTLHours: 48,
		Encryption: config.CastEncryptionConfig{Enabled: true, KeyEnv: "GOFER_TEST_SERVE_CAST_KEY"},
	}
	if _, err := buildCastRecorder(cfg); err == nil {
		t.Fatal("expected fail-fast for a missing cast encryption key")
	}
}

// Enabled + encryption + a too-short key fails serve assembly fast.
func TestBuildCastRecorderEncryptedShortKey(t *testing.T) {
	short := make([]byte, 16)
	t.Setenv("GOFER_TEST_SERVE_CAST_KEY", base64.StdEncoding.EncodeToString(short))
	cfg := &config.Config{}
	cfg.Storage.Cast = config.CastConfig{
		Enabled: true, RetentionTTLHours: 48,
		Encryption: config.CastEncryptionConfig{Enabled: true, KeyEnv: "GOFER_TEST_SERVE_CAST_KEY"},
	}
	if _, err := buildCastRecorder(cfg); err == nil {
		t.Fatal("expected fail-fast for a short cast encryption key")
	}
}

// buildGateCore stands up a minimal serve Core (store + job service) for the prune
// gate tests, with an expired closed pty-session pointing at a real cast file on
// disk. It returns the core and the cast file path.
func buildGateCore(t *testing.T) (*core.Core, string) {
	t.Helper()
	cfg := &config.Config{Storage: config.StorageConfig{Root: t.TempDir()}}
	config.ApplyDefaults(cfg)
	cr, err := core.Build(cfg, core.WithAgentDetector(agent.NoopDetector{}))
	if err != nil {
		t.Fatalf("core.Build: %v", err)
	}
	t.Cleanup(func() { _ = cr.Close() })

	castPath := filepath.Join(t.TempDir(), "pty.cast")
	if err := os.WriteFile(castPath, []byte("cast-bytes"), 0o600); err != nil {
		t.Fatalf("write cast file: %v", err)
	}
	now := time.Now()
	rec := jobstore.PtySessionRecord{
		PtySessionID: "gate1", JobID: "gjob1", State: "closed",
		RecordingURI: castPath, Encrypted: 1,
		StartedAt: now.Add(-200 * time.Hour).Unix(), EndedAt: now.Add(-100 * time.Hour).Unix(),
	}
	if err := cr.Store.UpsertPtySession(rec); err != nil {
		t.Fatalf("upsert pty session: %v", err)
	}
	return cr, castPath
}

// TestStartPruneLoopCastEnabledTriggersStart: with job/workflow retention OFF but
// castEnabled=true, the prune loop still starts and its startup prune runs the cast
// TTL sweep — the expired recording file is deleted (D-P3-6 gate: cast alone starts
// the loop).
func TestStartPruneLoopCastEnabledTriggersStart(t *testing.T) {
	cr, castPath := buildGateCore(t)
	cr.Config().Storage.Cast = config.CastConfig{Enabled: true, RetentionTTLHours: 24}

	stop := make(chan struct{})
	defer close(stop)
	startPruneLoop(gcli.NewCommand("t", ""), cr.Jobs, config.RetentionConfig{}, true, 24*3600, stop)

	// The startup prune runs async; poll until the expired cast file is gone.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(castPath); os.IsNotExist(err) {
			return // swept as expected
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("cast file not swept: prune loop did not start for castEnabled=true with retention off")
}

// TestStartPruneLoopGateOffNoStart: with retention OFF and castEnabled=false the
// loop must NOT start, so no sweep runs even though the config would otherwise
// enable one — proving the gate keys on the castEnabled argument (zero behaviour
// change when nothing is configured, G023).
func TestStartPruneLoopGateOffNoStart(t *testing.T) {
	cr, castPath := buildGateCore(t)
	// Config enables cast so a STARTED loop's sweep WOULD delete the file — the only
	// thing keeping it is the gate returning early on castEnabled=false.
	cr.Config().Storage.Cast = config.CastConfig{Enabled: true, RetentionTTLHours: 24}

	stop := make(chan struct{})
	defer close(stop)
	startPruneLoop(gcli.NewCommand("t", ""), cr.Jobs, config.RetentionConfig{}, false, 0, stop)

	// Give any (erroneously started) goroutine time to run its startup prune.
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(castPath); err != nil {
		t.Fatalf("cast file removed though the prune loop should not have started: %v", err)
	}
}
