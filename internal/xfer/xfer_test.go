package xfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// memRepo is an in-memory Repo so the xfer business rules are testable without
// SQLite.
type memRepo struct {
	mu   sync.Mutex
	rows map[string]jobstore.XferRecord
	seq  []string
}

func newMemRepo() *memRepo { return &memRepo{rows: map[string]jobstore.XferRecord{}} }

func (r *memRepo) InsertXfer(rec jobstore.XferRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.rows[rec.ID]; ok {
		return errors.New("duplicate id")
	}
	r.rows[rec.ID] = rec
	r.seq = append(r.seq, rec.ID)
	return nil
}

func (r *memRepo) GetXfer(id string) (jobstore.XferRecord, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.rows[id]
	return rec, ok, nil
}

func (r *memRepo) ListXfers(f jobstore.XferFilter) ([]jobstore.XferRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]jobstore.XferRecord, 0, len(r.seq))
	for _, id := range r.seq {
		rec := r.rows[id]
		if f.State != "" && rec.State != f.State {
			continue
		}
		if f.Runner != "" && rec.Runner != f.Runner {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

func (r *memRepo) UpdateXferState(id, state, errMsg string, finishedAt int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.rows[id]
	if !ok {
		return ErrNotFound
	}
	rec.State = state
	if errMsg != "" {
		rec.Error = errMsg
	}
	if finishedAt > 0 {
		rec.FinishedAt = finishedAt
	}
	r.rows[id] = rec
	return nil
}

func (r *memRepo) SetXferContent(id string, size int64, sha256 string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.rows[id]
	if !ok {
		return ErrNotFound
	}
	rec.Size, rec.SHA256 = size, sha256
	r.rows[id] = rec
	return nil
}

func (r *memRepo) DeleteXfer(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.rows, id)
	return nil
}

func (r *memRepo) ExpiredXfers(now int64) ([]jobstore.XferRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []jobstore.XferRecord
	for _, id := range r.seq {
		rec := r.rows[id]
		if rec.ExpiresAt > 0 && rec.ExpiresAt <= now && rec.State != string(StateExpired) {
			out = append(out, rec)
		}
	}
	return out, nil
}

// TestStoreWriteFinalizeReadRemove: the staging store's temp-then-rename
// discipline — an unfinalized write is invisible, a finalized one is readable and
// a removal takes the whole directory with it.
func TestStoreWriteFinalizeReadRemove(t *testing.T) {
	st := NewStore(filepath.Join(t.TempDir(), "xfer"))
	payload := []byte("staged-bytes")

	w, err := st.Writer("x1")
	if err != nil {
		t.Fatalf("Writer: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Not finalized yet: the payload must not be visible.
	if _, err := st.Reader("x1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reader before Finalize err=%v, want ErrNotFound", err)
	}

	if err := st.Finalize("x1", int64(len(payload)*2)); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if err := st.Finalize("x1", 3); err == nil {
		t.Fatalf("Finalize with a wrong size must fail")
	}
	rc, err := st.Reader("x1")
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, append(payload, payload...)) {
		t.Fatalf("payload = %q, want the doubled payload", got)
	}
	if n, err := st.Stat("x1"); err != nil || n != int64(len(payload)*2) {
		t.Fatalf("Stat = %d, %v", n, err)
	}

	if err := st.Remove("x1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(st.Dir("x1")); !os.IsNotExist(err) {
		t.Fatalf("staging dir survived Remove (err=%v)", err)
	}
	// A second Remove is not an error.
	if err := st.Remove("x1"); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
}

// TestStoreSweepExpires: the disk sweep removes only the directories older than
// the TTL, so a transfer written moments ago survives.
func TestStoreSweepExpires(t *testing.T) {
	root := filepath.Join(t.TempDir(), "xfer")
	st := NewStore(root)
	for _, id := range []string{"old", "fresh"} {
		if err := st.Create(id); err != nil {
			t.Fatalf("Create(%s): %v", id, err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(st.Dir("old"), old, old); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	removed, err := st.Sweep(time.Now(), 24*time.Hour)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(removed) != 1 || removed[0] != "old" {
		t.Fatalf("removed = %v, want [old]", removed)
	}
	if _, err := os.Stat(st.Dir("old")); !os.IsNotExist(err) {
		t.Fatalf("expired dir survived the sweep")
	}
	if _, err := os.Stat(st.Dir("fresh")); err != nil {
		t.Fatalf("fresh dir was swept: %v", err)
	}
}

// TestManagerRejectsOversize: a transfer over the configured cap is refused
// before anything is written, and no journal row is left behind.
func TestManagerRejectsOversize(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	repo := newMemRepo()
	mgr, err := NewManager(Options{
		Root:   t.TempDir(),
		Limits: Limits{MaxBytes: 8, TTL: time.Hour},
		Repo:   repo,
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	_, err = mgr.StagePut("alice", "w-1", "demo", "tmp/a.bin", 9, "aa", false)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("StagePut err=%v, want ErrTooLarge", err)
	}
	if !strings.Contains(err.Error(), "8") {
		t.Fatalf("error %q should name the limit", err)
	}
	if rows, _ := mgr.List(jobstore.XferFilter{}); len(rows) != 0 {
		t.Fatalf("rejected transfer left %d rows", len(rows))
	}

	// At the limit it is accepted, defaults fill in and expiry is stamped.
	rec, err := mgr.StagePut("alice", "server", "demo", "tmp/a.bin", 8, "aa", true)
	if err != nil {
		t.Fatalf("StagePut at the limit: %v", err)
	}
	if rec.Runner != "local" {
		t.Fatalf("runner = %q, want the local alias normalized", rec.Runner)
	}
	if rec.Force != 1 || rec.State != string(StateStaged) {
		t.Fatalf("record = %+v, want force=1 state=staged", rec)
	}
	if want := now.Add(time.Hour).Unix(); rec.ExpiresAt != want {
		t.Fatalf("expires_at = %d, want %d", rec.ExpiresAt, want)
	}
}

// TestManagerDeliverSettlesAndExpires covers the orchestration rules the HTTP and
// worker paths rely on: Deliver is idempotent, a runner failure is recorded
// verbatim (ErrExists → "exists") and expiry removes both the row's state and the
// staged bytes.
func TestManagerDeliverSettlesAndExpires(t *testing.T) {
	root := t.TempDir()
	repo := newMemRepo()
	now := time.Unix(1_700_000_000, 0)
	count := 0
	mgr, err := NewManager(Options{
		Root:   root,
		Limits: Limits{MaxBytes: 1 << 20, TTL: time.Hour},
		Repo:   repo,
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	var lastRec jobstore.XferRecord
	mgr.SetRunner(runnerFunc(func(_ context.Context, rec jobstore.XferRecord) error {
		count++
		lastRec = rec
		if rec.Path == "tmp/busy.bin" {
			return ErrExists
		}
		return nil
	}))

	okRec, err := mgr.StageGet("alice", "w-1", "demo", "tmp/out.bin")
	if err != nil {
		t.Fatalf("StageGet: %v", err)
	}
	if err := mgr.Deliver(context.Background(), okRec.ID); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	settled, _, _ := repo.GetXfer(okRec.ID)
	if settled.State != string(StateDone) || settled.FinishedAt != now.Unix() {
		t.Fatalf("record = %+v, want done at %d", settled, now.Unix())
	}
	if count != 1 || lastRec.ID != okRec.ID {
		t.Fatalf("runner calls = %d (last %s), want 1 for %s", count, lastRec.ID, okRec.ID)
	}
	// A second Deliver is a no-op: a settled transfer is never re-run.
	if err := mgr.Deliver(context.Background(), okRec.ID); err != nil {
		t.Fatalf("second Deliver: %v", err)
	}
	if count != 1 {
		t.Fatalf("runner calls = %d after a repeat Deliver, want 1", count)
	}

	busy, err := mgr.StageGet("alice", "w-1", "demo", "tmp/busy.bin")
	if err != nil {
		t.Fatalf("StageGet(busy): %v", err)
	}
	if err := mgr.Deliver(context.Background(), busy.ID); err != nil {
		t.Fatalf("Deliver(busy): %v", err)
	}
	failed, _, _ := repo.GetXfer(busy.ID)
	if failed.State != string(StateFailed) || failed.Error != "exists" {
		t.Fatalf("record = %+v, want failed/exists", failed)
	}

	// Expiry: only the row past its TTL is expired, and its staging directory goes.
	w, err := mgr.Store().Writer(busy.ID)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	w.Close()
	if n, err := mgr.Expire(now.Add(2 * time.Hour)); err != nil || n != 2 {
		t.Fatalf("Expire = %d, %v; want 2 expired", n, err)
	}
	for _, id := range []string{okRec.ID, busy.ID} {
		rec, _, _ := repo.GetXfer(id)
		if rec.State != string(StateExpired) {
			t.Fatalf("record %s state = %s, want expired", id, rec.State)
		}
		if _, err := os.Stat(mgr.Store().Dir(id)); !os.IsNotExist(err) {
			t.Fatalf("staging for %s survived expiry", id)
		}
	}
}

type runnerFunc func(context.Context, jobstore.XferRecord) error

func (f runnerFunc) Run(ctx context.Context, rec jobstore.XferRecord) error { return f(ctx, rec) }
