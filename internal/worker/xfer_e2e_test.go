package worker_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/wshub"
	"github.com/inhere/gofer/internal/xfer"
)

// xferHubSide is the serve side of the XFER-01 e2e. It is built on a REAL core, so the
// transfer manager, its Router and the hub adapter that converts a transfer request
// onto the wire are exactly the production wiring (core.buildXferManager); only the
// HTTP surface is local to the test.
type xferHubSide struct {
	ts    *httptest.Server
	hub   *wshub.Hub
	mgr   *xfer.Manager
	store *jobstore.Store
}

// buildXferHubSide stands up a serve side whose httptest server serves BOTH the
// worker's WS route and the two content endpoints. One origin is not a convenience: the
// worker derives its HTTP base from the WS session URL it registered on, so the content
// endpoints MUST live where the hub lives (that is the design — §一.2 — and it is what
// keeps a transfer from needing a second address).
func buildXferHubSide(t *testing.T) *xferHubSide {
	t.Helper()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Token:   "server-default-token",
			Workers: map[string]config.WorkerAuthConfig{e2eWorkerID: {Token: e2eToken}},
		},
		Storage: config.StorageConfig{Root: t.TempDir()},
		// The hub-side project entry exists for the transfer journal (the executing
		// machine resolves the path against ITS OWN root, which the worker fixture owns).
		Projects: map[string]config.ProjectConfig{"alpha": {HostPath: t.TempDir()}},
	}
	config.ApplyDefaults(cfg)
	cr, err := core.Build(cfg, core.WithAgentDetector(agent.NoopDetector{}))
	if err != nil {
		t.Fatalf("core.Build: %v", err)
	}
	t.Cleanup(func() { _ = cr.Close() })

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/workers/connect", func(w http.ResponseWriter, r *http.Request) {
		cr.Hub.Accept(w, r, e2eWorkerID)
	})
	mux.Handle("/v1/xfer/", xferContentHandler(t, cr.Xfer()))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &xferHubSide{ts: ts, hub: cr.Hub, mgr: cr.Xfer(), store: cr.Store}
}

// xferContentHandler is the two content endpoints the worker calls, served straight off
// the transfer manager's staging store. It MIRRORS the contract internal/httpapi
// serves — Bearer token, "only the transfer assigned to THIS worker", the
// X-Gofer-Size/X-Gofer-Sha256 verification on upload and the verified-upload-settles-it
// rule — but lives here so the worker e2e depends on the manager alone.
func xferContentHandler(t *testing.T, mgr *xfer.Manager) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/xfer/{id}/content", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+e2eToken {
			http.Error(w, "worker token required", http.StatusUnauthorized)
			return
		}
		rec, ok, err := mgr.Get(r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "unknown xfer", http.StatusNotFound)
			return
		}
		if rec.Runner != e2eWorkerID || rec.Op != string(xfer.OpPut) {
			http.Error(w, "not your transfer", http.StatusForbidden)
			return
		}
		f, err := mgr.Store().Reader(rec.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		defer f.Close()
		if rec.Size > 0 {
			w.Header().Set("X-Gofer-Size", strconv.FormatInt(rec.Size, 10))
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		if _, err := io.Copy(w, f); err != nil {
			t.Logf("content stream aborted: %v", err)
		}
	})
	mux.HandleFunc("PUT /v1/xfer/{id}/content", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+e2eToken {
			http.Error(w, "worker token required", http.StatusUnauthorized)
			return
		}
		rec, ok, err := mgr.Get(r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "unknown xfer", http.StatusNotFound)
			return
		}
		if rec.Runner != e2eWorkerID || rec.Op != string(xfer.OpGet) {
			http.Error(w, "not your transfer", http.StatusForbidden)
			return
		}
		dst, err := mgr.Store().Writer(rec.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sum := sha256.New()
		n, cerr := io.Copy(io.MultiWriter(dst, sum), r.Body)
		closeErr := dst.Close()
		if cerr != nil || closeErr != nil {
			reason := cerr
			if reason == nil {
				reason = closeErr
			}
			http.Error(w, reason.Error(), http.StatusInternalServerError)
			return
		}
		got := hex.EncodeToString(sum.Sum(nil))
		if want := r.Header.Get("X-Gofer-Sha256"); want != "" && !strings.EqualFold(want, got) {
			http.Error(w, "sha256 mismatch", http.StatusBadRequest)
			return
		}
		if want := r.Header.Get("X-Gofer-Size"); want != "" {
			if wantN, perr := strconv.ParseInt(want, 10, 64); perr == nil && wantN != n {
				http.Error(w, "size mismatch", http.StatusBadRequest)
				return
			}
		}
		if err := mgr.CommitGet(rec.ID, n, got); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := mgr.MarkDone(rec.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// startXferWorker brings up the hub side plus an in-process worker and waits for its
// registration. It returns the worker's project root (the directory the worker's own
// config points project "alpha" at), which is where a put must land.
func startXferWorker(t *testing.T, ctx context.Context) (*xferHubSide, string) {
	t.Helper()
	hub := buildXferHubSide(t)
	var workerHost string
	cl, _ := buildWorkerSideJobsOpts(t, hub.ts.URL, workerSideOpts{
		PrepareHost: func(host string) { workerHost = host },
	})
	go func() { _ = cl.Run(ctx) }()
	waitWorkerOnline(t, hub.hub)
	if workerHost == "" {
		t.Fatal("worker fixture did not report its project root")
	}
	return hub, workerHost
}

// stagePutPayload stages a complete put payload exactly like the CLI's push does
// (stream into the staging area, then verify and commit it).
func stagePutPayload(t *testing.T, mgr *xfer.Manager, project, path string, payload []byte, force bool) jobstore.XferRecord {
	t.Helper()
	sum := sha256Hex(payload)
	rec, err := mgr.StagePut("e2e-tester", e2eWorkerID, project, path, int64(len(payload)), sum, force)
	if err != nil {
		t.Fatalf("StagePut: %v", err)
	}
	w, err := mgr.Store().Writer(rec.ID)
	if err != nil {
		t.Fatalf("Store().Writer: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("write staged payload: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close staged payload: %v", err)
	}
	if err := mgr.CommitPut(rec.ID, int64(len(payload)), sum); err != nil {
		t.Fatalf("CommitPut: %v", err)
	}
	return rec
}

// randomPayload returns n deterministic pseudo-random bytes (a real payload, without a
// per-run flake or a cryptographic RNG's cost on 5MB).
func randomPayload(n int) []byte {
	b := make([]byte, n)
	rng := rand.New(rand.NewPCG(2026, 921))
	for i := range b {
		b[i] = byte(rng.IntN(256))
	}
	return b
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// xferState reads one journal row and fails when it is not in want.
func xferRow(t *testing.T, mgr *xfer.Manager, id string) jobstore.XferRecord {
	t.Helper()
	rec, ok, err := mgr.Get(id)
	if err != nil || !ok {
		t.Fatalf("Get(%s): ok=%v err=%v", id, ok, err)
	}
	return rec
}

// TestFileXferPutToWorker is the XFER-01 push acceptance: a 5MB payload staged on the
// server ends up byte-identical (sha256-verified) inside the WORKER's project directory,
// and the transfer is recorded done + audited.
func TestFileXferPutToWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hub, workerHost := startXferWorker(t, ctx)

	payload := randomPayload(5 << 20)
	sum := sha256Hex(payload)
	rec := stagePutPayload(t, hub.mgr, "alpha", "tmp/in/firmware.bin", payload, false)

	if err := hub.mgr.Deliver(ctx, rec.ID); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	row := xferRow(t, hub.mgr, rec.ID)
	if row.State != string(xfer.StateDone) {
		t.Fatalf("put state = %s (error=%q), want done", row.State, row.Error)
	}

	dst := filepath.Join(workerHost, "tmp", "in", "firmware.bin")
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read transferred file: %v", err)
	}
	if len(got) != len(payload) {
		t.Fatalf("transferred size = %d, want %d", len(got), len(payload))
	}
	if gotSum := sha256Hex(got); gotSum != sum {
		t.Fatalf("transferred sha256 = %s, want %s", gotSum, sum)
	}
	// No temp file may be left behind next to the destination.
	if leftovers, _ := filepath.Glob(dst + ".gofer-tmp-*"); len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}

	// Audit: the transfer recorded exactly one xfer.put event against its synthetic id.
	events, err := hub.store.ListJobEvents(xfer.EventJobID(rec.ID), 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	if len(events) != 1 || events[0].Type != "xfer.put" {
		t.Fatalf("events = %+v, want exactly one xfer.put", events)
	}
	if !strings.Contains(events[0].Detail, sha256Hex(payload)) {
		t.Fatalf("xfer.put detail = %s, want it to carry the payload sha256", events[0].Detail)
	}
}

// TestFileXferGetFromWorker is the pull direction: a file in the WORKER's project
// directory is staged on the server, byte-identical and settled done.
func TestFileXferGetFromWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hub, workerHost := startXferWorker(t, ctx)

	src := filepath.Join(workerHost, "tmp", "out", "report.csv")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	content := []byte("col,...\n1,2\n")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	rec, err := hub.mgr.StageGet("e2e-tester", e2eWorkerID, "alpha", "tmp/out/report.csv")
	if err != nil {
		t.Fatalf("StageGet: %v", err)
	}
	if err := hub.mgr.Deliver(ctx, rec.ID); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	row := xferRow(t, hub.mgr, rec.ID)
	if row.State != string(xfer.StateDone) {
		t.Fatalf("get state = %s (error=%q), want done", row.State, row.Error)
	}
	if row.Size != int64(len(content)) || row.SHA256 != sha256Hex(content) {
		t.Fatalf("journal content = (%d, %s), want (%d, %s)", row.Size, row.SHA256, len(content), sha256Hex(content))
	}
	rc, err := hub.mgr.Store().Reader(rec.ID)
	if err != nil {
		t.Fatalf("Reader(staged get): %v", err)
	}
	defer rc.Close()
	staged, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read staged payload: %v", err)
	}
	if string(staged) != string(content) {
		t.Fatalf("staged payload = %q, want %q", staged, content)
	}
}

// TestFileXferRejectsEscapeOnWorker proves the executing machine re-validates the
// dispatch against its OWN project root (review #8): a path that escapes it fails the
// transfer, and nothing outside the root is created.
func TestFileXferRejectsEscapeOnWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hub, workerHost := startXferWorker(t, ctx)

	payload := []byte("should never land")
	rec := stagePutPayload(t, hub.mgr, "alpha", "../../etc/passwd", payload, false)
	if err := hub.mgr.Deliver(ctx, rec.ID); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	row := xferRow(t, hub.mgr, rec.ID)
	if row.State != string(xfer.StateFailed) {
		t.Fatalf("escaping put state = %s, want failed", row.State)
	}
	if !strings.Contains(row.Error, "escapes project") {
		t.Fatalf("escaping put error = %q, want it to name the escape", row.Error)
	}
	// ../../etc/passwd relative to <tmp>/<project> resolves outside the temp tree; the
	// file must not exist anywhere near it.
	outside := filepath.Join(filepath.Dir(workerHost), "..", "etc", "passwd")
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("escaping transfer wrote %s", outside)
	}
}

// TestFileXferForceOverwrite is the force switch: an existing destination without force
// fails with the literal reason "exists" (the contract the CLI matches on) and is left
// untouched; with force it is replaced.
func TestFileXferForceOverwrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hub, workerHost := startXferWorker(t, ctx)

	dst := filepath.Join(workerHost, "tmp", "in", "x.bin")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir destination dir: %v", err)
	}
	old := []byte("original")
	if err := os.WriteFile(dst, old, 0o644); err != nil {
		t.Fatalf("write existing destination: %v", err)
	}
	fresh := []byte("replacement")

	refused := stagePutPayload(t, hub.mgr, "alpha", "tmp/in/x.bin", fresh, false)
	if err := hub.mgr.Deliver(ctx, refused.ID); err != nil {
		t.Fatalf("Deliver(no force): %v", err)
	}
	row := xferRow(t, hub.mgr, refused.ID)
	if row.State != string(xfer.StateFailed) || row.Error != "exists" {
		t.Fatalf("no-force put = (%s, %q), want (failed, \"exists\")", row.State, row.Error)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != string(old) {
		t.Fatalf("destination after a refused put = %q, %v (want it untouched)", got, err)
	}

	forced := stagePutPayload(t, hub.mgr, "alpha", "tmp/in/x.bin", fresh, true)
	if err := hub.mgr.Deliver(ctx, forced.ID); err != nil {
		t.Fatalf("Deliver(force): %v", err)
	}
	if row := xferRow(t, hub.mgr, forced.ID); row.State != string(xfer.StateDone) {
		t.Fatalf("forced put state = %s (error=%q), want done", row.State, row.Error)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != string(fresh) {
		t.Fatalf("destination after a forced put = %q, %v (want it replaced)", got, err)
	}
}

// TestFileXferUnknownProjectFails guards the other half of the worker-side resolution:
// a project this worker does not know is refused by name, without touching the disk.
func TestFileXferUnknownProjectFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hub, workerHost := startXferWorker(t, ctx)

	rec := stagePutPayload(t, hub.mgr, "ghost", "tmp/x.bin", []byte("x"), false)
	if err := hub.mgr.Deliver(ctx, rec.ID); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	row := xferRow(t, hub.mgr, rec.ID)
	if row.State != string(xfer.StateFailed) || !strings.Contains(row.Error, "ghost") {
		t.Fatalf("unknown-project put = (%s, %q), want a failed transfer naming the project", row.State, row.Error)
	}
	if _, err := os.Stat(filepath.Join(workerHost, "tmp", "x.bin")); err == nil {
		t.Fatal("a transfer for an unknown project still wrote a file")
	}
}

// TestFileXferWorkerOfflineFails keeps the "never queue" rule honest end to end: with no
// worker registered the dispatch fails at once, with the reason the operator needs.
func TestFileXferWorkerOfflineFails(t *testing.T) {
	hub := buildXferHubSide(t)
	rec := stagePutPayload(t, hub.mgr, "alpha", "tmp/in/a.bin", []byte("payload"), false)
	// Deliver reports the dispatch's own failure (there is none — the transfer was
	// driven), so the OUTCOME is read from the journal, exactly as the CLI does.
	if err := hub.mgr.Deliver(context.Background(), rec.ID); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	row := xferRow(t, hub.mgr, rec.ID)
	if row.State != string(xfer.StateFailed) {
		t.Fatalf("state = %s, want failed", row.State)
	}
	if !strings.Contains(row.Error, "worker offline") {
		t.Fatalf("error = %q, want it to report the worker as offline", row.Error)
	}
}
