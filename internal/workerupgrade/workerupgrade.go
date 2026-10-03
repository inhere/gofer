// Package workerupgrade is the server-side bookkeeping of remote worker binary
// upgrades (design 2026-10-03-worker-remote-upgrade, U1): it stages the binary a
// worker will download (one file per worker id under <run>/upgrade/) and records the
// outcome of the latest upgrade per worker. It knows nothing about HTTP or the
// websocket hub; httpapi serves the files and the hub reports into it.
package workerupgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// Upgrade states. Pending is the only non-terminal one.
const (
	StatePending    = "pending"
	StateSucceeded  = "succeeded"
	StateRolledBack = "rolled_back"
	StateFailed     = "failed"
)

// MaxBinaryBytes caps one staged upload (a gofer binary is a few tens of MB).
const MaxBinaryBytes int64 = 512 << 20

// ErrNotStaged means no binary has been staged for the worker.
var ErrNotStaged = errors.New("no upgrade binary staged for this worker")

// ErrInProgress means the worker already has a pending upgrade.
var ErrInProgress = errors.New("an upgrade is already in progress for this worker")

var safeID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Staged describes the binary staged for one worker. SHA256 and Size are computed by
// the server from the stored bytes, never taken from the uploader.
type Staged struct {
	WorkerID string `json:"worker_id"`
	Path     string `json:"-"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

// Record is the latest upgrade attempt of one worker. Times are unix milliseconds.
type Record struct {
	WorkerID      string `json:"worker_id"`
	UpgradeID     string `json:"upgrade_id"`
	State         string `json:"state"`
	FromVersion   string `json:"from_version,omitempty"`
	TargetVersion string `json:"target_version,omitempty"`
	TargetSHA256  string `json:"target_sha256,omitempty"`
	Force         bool   `json:"force,omitempty"`
	Error         string `json:"error,omitempty"`
	StartedAt     int64  `json:"started_at"`
	FinishedAt    int64  `json:"finished_at,omitempty"`
	DurationMS    int64  `json:"duration_ms,omitempty"`
}

// Manager stages upgrade binaries and tracks upgrade records. Records are kept in
// memory and mirrored to <dir>/<worker>.json so the last result survives a server
// restart. Safe for concurrent use.
type Manager struct {
	dir string
	now func() time.Time

	mu   sync.Mutex
	recs map[string]*Record
}

// New builds a Manager rooted at dir (created lazily).
func New(dir string) *Manager {
	return &Manager{dir: dir, now: time.Now, recs: map[string]*Record{}}
}

// SetClock replaces the time source (tests).
func (m *Manager) SetClock(now func() time.Time) { m.now = now }

func (m *Manager) binPath(id string) string  { return filepath.Join(m.dir, id+".bin") }
func (m *Manager) metaPath(id string) string { return filepath.Join(m.dir, id+".json") }

func checkID(id string) error {
	if id == "" || !safeID.MatchString(id) || id == "." || id == ".." {
		return fmt.Errorf("invalid worker id %q", id)
	}
	return nil
}

// Stage stores the stream as worker id's upgrade binary (replacing any previous one)
// and returns its server-computed digest. At most max bytes are accepted.
func (m *Manager) Stage(id string, r io.Reader, max int64) (Staged, error) {
	if err := checkID(id); err != nil {
		return Staged{}, err
	}
	if max <= 0 {
		max = MaxBinaryBytes
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return Staged{}, fmt.Errorf("create upgrade dir: %w", err)
	}
	tmp, err := os.CreateTemp(m.dir, id+".*.part")
	if err != nil {
		return Staged{}, fmt.Errorf("create staging file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpName) }
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, max+1))
	if err != nil {
		cleanup()
		return Staged{}, fmt.Errorf("receive upgrade binary: %w", err)
	}
	if n > max {
		cleanup()
		return Staged{}, fmt.Errorf("upgrade binary exceeds %d bytes", max)
	}
	if n == 0 {
		cleanup()
		return Staged{}, errors.New("upgrade binary is empty")
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return Staged{}, err
	}
	if err := os.Rename(tmpName, m.binPath(id)); err != nil {
		_ = os.Remove(tmpName)
		return Staged{}, fmt.Errorf("publish upgrade binary: %w", err)
	}
	return Staged{WorkerID: id, Path: m.binPath(id), SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}

// StageFile stages a copy of the local file at path (the server's own executable).
func (m *Manager) StageFile(id, path string) (Staged, error) {
	f, err := os.Open(path)
	if err != nil {
		return Staged{}, err
	}
	defer f.Close()
	return m.Stage(id, f, 0)
}

// Staged returns the staged binary of worker id, digesting the stored bytes.
func (m *Manager) Staged(id string) (Staged, error) {
	if err := checkID(id); err != nil {
		return Staged{}, err
	}
	f, err := os.Open(m.binPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return Staged{}, ErrNotStaged
	}
	if err != nil {
		return Staged{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return Staged{}, err
	}
	return Staged{WorkerID: id, Path: m.binPath(id), SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}

// Open opens the staged binary for reading, with its digest.
func (m *Manager) Open(id string) (*os.File, Staged, error) {
	st, err := m.Staged(id)
	if err != nil {
		return nil, Staged{}, err
	}
	f, err := os.Open(st.Path)
	if err != nil {
		return nil, Staged{}, err
	}
	return f, st, nil
}

// Begin records a new pending upgrade for worker id. It refuses while a previous
// attempt is still pending (a stale pending record older than staleAfter is
// superseded so a lost worker cannot block upgrades forever).
func (m *Manager) Begin(id, upgradeID, fromVersion, targetVersion, targetSHA string, force bool, staleAfter time.Duration) (Record, error) {
	if err := checkID(id); err != nil {
		return Record{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur := m.loadLocked(id); cur != nil && cur.State == StatePending {
		started := time.UnixMilli(cur.StartedAt)
		if staleAfter <= 0 || m.now().Sub(started) < staleAfter {
			return *cur, ErrInProgress
		}
	}
	rec := &Record{
		WorkerID: id, UpgradeID: upgradeID, State: StatePending,
		FromVersion: fromVersion, TargetVersion: targetVersion, TargetSHA256: targetSHA,
		Force: force, StartedAt: m.now().UnixMilli(),
	}
	m.recs[id] = rec
	m.saveLocked(rec)
	return *rec, nil
}

// Finish moves the pending upgrade upgradeID of worker id to a terminal state. A
// stale report (different upgrade id) is ignored; so is one for an upgrade that is
// already terminal, except that a successful registration always wins (the new
// process being online is the ground truth).
func (m *Manager) Finish(id, upgradeID, state, errMsg, version string) (Record, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.loadLocked(id)
	if cur == nil || cur.UpgradeID != upgradeID {
		return Record{}, false
	}
	if cur.State != StatePending && state != StateSucceeded {
		return *cur, false
	}
	cur.State = state
	cur.Error = errMsg
	if state == StateSucceeded {
		cur.Error = ""
		if version != "" {
			cur.TargetVersion = version
		}
	} else if version != "" && cur.TargetVersion == "" {
		cur.TargetVersion = version
	}
	cur.FinishedAt = m.now().UnixMilli()
	cur.DurationMS = cur.FinishedAt - cur.StartedAt
	m.saveLocked(cur)
	return *cur, true
}

// SetTargetVersion records the version the worker reported for the staged binary.
func (m *Manager) SetTargetVersion(id, upgradeID, version string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur := m.loadLocked(id); cur != nil && cur.UpgradeID == upgradeID && version != "" {
		cur.TargetVersion = version
		m.saveLocked(cur)
	}
}

// Latest returns the most recent upgrade record of worker id.
func (m *Manager) Latest(id string) (Record, bool) {
	if checkID(id) != nil {
		return Record{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.loadLocked(id)
	if cur == nil {
		return Record{}, false
	}
	return *cur, true
}

func (m *Manager) loadLocked(id string) *Record {
	if rec, ok := m.recs[id]; ok {
		return rec
	}
	data, err := os.ReadFile(m.metaPath(id))
	if err != nil {
		return nil
	}
	var rec Record
	if json.Unmarshal(data, &rec) != nil || rec.UpgradeID == "" {
		return nil
	}
	m.recs[id] = &rec
	return &rec
}

func (m *Manager) saveLocked(rec *Record) {
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if os.MkdirAll(m.dir, 0o755) != nil {
		return
	}
	tmp := m.metaPath(rec.WorkerID) + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, m.metaPath(rec.WorkerID))
	}
}
