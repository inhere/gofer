package xfer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// Repo is the journal persistence seam, satisfied by *jobstore.Store. It is
// declared here (rather than the Manager holding a concrete store) so the xfer
// business logic is testable without SQLite and so the HTTP layer can build a
// Manager over a stub.
type Repo interface {
	InsertXfer(jobstore.XferRecord) error
	GetXfer(id string) (jobstore.XferRecord, bool, error)
	ListXfers(jobstore.XferFilter) ([]jobstore.XferRecord, error)
	UpdateXferState(id, state, errMsg string, finishedAt int64) error
	SetXferContent(id string, size int64, sha256 string) error
	DeleteXfer(id string) error
	ExpiredXfers(now int64) ([]jobstore.XferRecord, error)
}

// EventSink is the audit seam onto the append-only event log, satisfied by
// *jobstore.Store. A transfer is not a job, so its events are recorded under the
// synthetic job id `xfer:<id>`: the durable log, the SSE/poll cursor and the
// audit trail are shared with job events, while the id keeps them out of every
// real job's stream.
type EventSink interface {
	InsertJobEvent(jobstore.JobEvent) (int64, error)
}

// EventJobID is the synthetic event-log owner id of one transfer.
func EventJobID(id string) string { return "xfer:" + id }

// Runner executes one already-staged transfer on its executing machine. The
// server-local implementation and the worker-hub adapter both satisfy it;
// returning nil means the bytes are in place, and any other error is the
// terminal failure reason (ErrExists is surfaced as the literal "exists").
type Runner interface {
	Run(ctx context.Context, rec jobstore.XferRecord) error
}

// Options configures a Manager.
type Options struct {
	// Root is the base directory the staging area is created under: the Manager
	// owns <Root>/xfer. serve passes storage.root when set, else the config dir.
	Root string
	// Limits are the byte/TTL caps; zero fields fall back to DefaultLimits.
	Limits Limits
	// Repo is the journal (required).
	Repo Repo
	// Events is the optional audit sink; nil disables event recording.
	Events EventSink
	// Now is the clock seam (tests); nil = time.Now.
	Now func() time.Time
}

// Manager carries out the transfer business operations over a Store and a Repo.
type Manager struct {
	store  *Store
	repo   Repo
	events EventSink
	limits Limits
	nowFn  func() time.Time

	mu     sync.Mutex
	runner Runner
}

// NewManager builds a Manager from opts. It fails when no journal (Repo) is
// given: every transfer must be recorded.
func NewManager(opts Options) (*Manager, error) {
	if opts.Repo == nil {
		return nil, fmt.Errorf("xfer: NewManager: Repo is required")
	}
	if opts.Root == "" {
		return nil, fmt.Errorf("xfer: NewManager: Root is required")
	}
	lim := opts.Limits
	def := DefaultLimits()
	if lim.MaxBytes <= 0 {
		lim.MaxBytes = def.MaxBytes
	}
	if lim.TTL <= 0 {
		lim.TTL = def.TTL
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Manager{
		store:  NewStore(filepath.Join(opts.Root, "xfer")),
		repo:   opts.Repo,
		events: opts.Events,
		limits: lim,
		nowFn:  now,
	}, nil
}

// Limits returns the effective caps (the HTTP layer answers 413 from MaxBytes).
func (m *Manager) Limits() Limits { return m.limits }

// Store exposes the staging area (the HTTP layer streams payloads through it).
func (m *Manager) Store() *Store { return m.store }

// SetRunner installs the executor for staged transfers. It is wired at assembly
// time (serve); without one a staged transfer stays staged.
func (m *Manager) SetRunner(r Runner) {
	m.mu.Lock()
	m.runner = r
	m.mu.Unlock()
}

func (m *Manager) runnerOrNil() Runner {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runner
}

// StagePut records a pending upload of size bytes towards
// <runner>:<project>/<path>. The payload is streamed into the staging area by the
// caller (Store().Writer) and made visible with CommitPut. force allows
// overwriting an existing destination.
func (m *Manager) StagePut(caller, runner, projectKey, path string, size int64, sha256 string, force bool) (jobstore.XferRecord, error) {
	if size > m.limits.MaxBytes {
		return jobstore.XferRecord{}, fmt.Errorf("%w: %d bytes exceeds %d", ErrTooLarge, size, m.limits.MaxBytes)
	}
	rec := jobstore.XferRecord{
		ID:         NewID(),
		Op:         string(OpPut),
		Runner:     config.NormalizeRunnerName(runner),
		ProjectKey: projectKey,
		Path:       path,
		Size:       size,
		SHA256:     sha256,
		State:      string(StateStaged),
		CallerID:   caller,
		CreatedAt:  m.nowFn().Unix(),
		ExpiresAt:  m.nowFn().Add(m.limits.TTL).Unix(),
	}
	if force {
		rec.Force = 1
	}
	if err := m.store.Create(rec.ID); err != nil {
		return jobstore.XferRecord{}, err
	}
	if err := m.repo.InsertXfer(rec); err != nil {
		_ = m.store.Remove(rec.ID)
		return jobstore.XferRecord{}, err
	}
	return rec, nil
}

// StageGet records a transfer that fetches <runner>:<project>/<path> back to the
// server. The staging directory is created up front so the executing worker can
// upload the result into it.
func (m *Manager) StageGet(caller, runner, projectKey, path string) (jobstore.XferRecord, error) {
	rec := jobstore.XferRecord{
		ID:         NewID(),
		Op:         string(OpGet),
		Runner:     config.NormalizeRunnerName(runner),
		ProjectKey: projectKey,
		Path:       path,
		State:      string(StateStaged),
		CallerID:   caller,
		CreatedAt:  m.nowFn().Unix(),
		ExpiresAt:  m.nowFn().Add(m.limits.TTL).Unix(),
	}
	if err := m.store.Create(rec.ID); err != nil {
		return jobstore.XferRecord{}, err
	}
	if err := m.repo.InsertXfer(rec); err != nil {
		_ = m.store.Remove(rec.ID)
		return jobstore.XferRecord{}, err
	}
	return rec, nil
}

// CommitPut makes a verified upload visible: the temporary payload is renamed
// into place and the journal records the confirmed size + sha256. The caller has
// already compared the streamed digest against the value the client declared, so
// a mismatch never reaches here.
func (m *Manager) CommitPut(id string, size int64, sha256 string) error {
	if err := m.store.Finalize(id, size); err != nil {
		return err
	}
	return m.repo.SetXferContent(id, size, sha256)
}

// CommitGet records the payload a worker uploaded for a get (its size + sha256),
// making it visible for the client to download.
func (m *Manager) CommitGet(id string, size int64, sha256 string) error {
	if err := m.store.Finalize(id, size); err != nil {
		return err
	}
	return m.repo.SetXferContent(id, size, sha256)
}

// Get returns one transfer row.
func (m *Manager) Get(id string) (jobstore.XferRecord, bool, error) { return m.repo.GetXfer(id) }

// List returns transfer rows newest-first.
func (m *Manager) List(f jobstore.XferFilter) ([]jobstore.XferRecord, error) {
	return m.repo.ListXfers(f)
}

// Remove deletes a transfer: its staging directory first (so a half-deleted
// transfer can never be downloaded), then its journal row.
func (m *Manager) Remove(id string) error {
	if err := m.store.Remove(id); err != nil {
		return err
	}
	return m.repo.DeleteXfer(id)
}

// Fail settles a transfer as failed and drops its staging directory (a failed
// transfer's partial bytes must never be served). Use it for a rejection detected
// before dispatch; MarkFailed handles the post-dispatch path.
func (m *Manager) Fail(id, reason string) error {
	_ = m.store.Remove(id)
	return m.repo.UpdateXferState(id, string(StateFailed), reason, m.nowFn().Unix())
}

// MarkDone settles a transfer as done and records its audit event exactly once.
func (m *Manager) MarkDone(id string) error {
	rec, ok, err := m.repo.GetXfer(id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	if State(rec.State).Terminal() {
		return nil
	}
	if err := m.repo.UpdateXferState(id, string(StateDone), "", m.nowFn().Unix()); err != nil {
		return err
	}
	m.recordEvent(rec, "xfer."+rec.Op)
	return nil
}

// MarkFailed settles a transfer as failed with reason, dropping its staging
// directory. A transfer already in a terminal state is left alone (idempotent),
// so a late worker result cannot overwrite a settled outcome.
func (m *Manager) MarkFailed(id, reason string) error {
	rec, ok, err := m.repo.GetXfer(id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	if State(rec.State).Terminal() {
		return nil
	}
	_ = m.store.Remove(id)
	return m.repo.UpdateXferState(id, string(StateFailed), reason, m.nowFn().Unix())
}

// Expire applies the TTL: every journal row past expires_at is marked expired and
// its staging directory removed, then orphan directories (a Create with no row,
// or a row the journal lost) older than the TTL are swept. It returns how many
// journal rows were expired.
func (m *Manager) Expire(now time.Time) (int, error) {
	rows, err := m.repo.ExpiredXfers(now.Unix())
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if err := m.repo.UpdateXferState(r.ID, string(StateExpired), "", now.Unix()); err != nil {
			return 0, err
		}
		_ = m.store.Remove(r.ID)
	}
	if swept, err := m.store.Sweep(now, m.limits.TTL); err != nil {
		slog.Warn("xfer.sweep_failed", "event", "xfer.sweep_failed", "component", "server", "err", err)
	} else if len(swept) > 0 {
		slog.Info("xfer.swept_orphans", "event", "xfer.swept_orphans", "component", "server", "count", len(swept))
	}
	return len(rows), nil
}

// recordEvent appends one transfer audit event (best-effort: an audit failure
// must never fail the transfer).
func (m *Manager) recordEvent(rec jobstore.XferRecord, eventType string) {
	if m.events == nil {
		return
	}
	detail := map[string]any{
		"xfer_id": rec.ID,
		"op":      rec.Op,
		"runner":  rec.Runner,
		"project": rec.ProjectKey,
		"path":    rec.Path,
		"size":    rec.Size,
		"sha256":  rec.SHA256,
		"by":      rec.CallerID,
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return
	}
	if _, err := m.events.InsertJobEvent(jobstore.JobEvent{
		JobID:  EventJobID(rec.ID),
		Type:   eventType,
		Detail: string(b),
		At:     m.nowFn().Unix(),
	}); err != nil {
		slog.Warn("xfer.event_failed", "event", "xfer.event_failed", "component", "server", "xfer_id", rec.ID, "err", err)
	}
}
