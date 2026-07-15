package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/inhere/gofer/internal/wsproto"
)

// Policy apply degrade gate strings surfaced on the Applied frame (diagnostic only).
const (
	// gateLegacyLocalProjects marks a worker that ignored a pushed policy because it
	// runs in LEGACY (local projects) mode (verification 1).
	gateLegacyLocalProjects = "legacy_local_projects"
	// gateCacheStale marks an applied policy whose last-known-good cache could not be
	// persisted, so a crash-restore may lag the in-memory Rev until the next apply /
	// reconnect (verification 9, cache-write-failure).
	gateCacheStale = "policy_cache_stale"
	// cacheRetryInterval is the backoff between best-effort retries of a failed cache
	// write. Liveness only — safety comes from invalidate-on-failure + reconnect
	// convergence, so this can be lazy.
	cacheRetryInterval = 15 * time.Second
)

// policyCacheFile is the on-disk last-known-good cache layout (T5-F). It is written
// atomically (temp + rename, 0600) so a reader (a cold-starting worker, or the T6
// CLI) never sees a half file. worker_id guards against a cache left by a different
// worker sharing the config dir; seq is the worker-local monotonic apply token used
// to drop a stale retry (never a cross-session Rev comparison).
type policyCacheFile struct {
	WorkerID  string          `json:"worker_id"`
	Rev       int64           `json:"rev"`
	Seq       uint64          `json:"seq"`
	WrittenAt int64           `json:"written_at"`
	Policy    *wsproto.Policy `json:"policy"`
}

// WritePolicyCacheFile atomically writes the last-known-good policy cache to path
// (same-dir temp + rename, 0600). Callers MkdirAll the parent. It is a pure file
// helper — the caller (writePolicyCacheGuarded) owns the token/ordering guards.
func WritePolicyCacheFile(path, workerID string, p *wsproto.Policy, seq uint64) error {
	if path == "" {
		return fmt.Errorf("policy cache: empty path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("policy cache mkdir: %w", err)
	}
	data, err := json.Marshal(policyCacheFile{
		WorkerID:  workerID,
		Rev:       revOf(p),
		Seq:       seq,
		WrittenAt: time.Now().Unix(),
		Policy:    p,
	})
	if err != nil {
		return fmt.Errorf("policy cache marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("policy cache temp: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup on any failure before the rename claims the temp.
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("policy cache chmod: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("policy cache write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("policy cache sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("policy cache close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("policy cache rename: %w", err)
	}
	return nil
}

// ReadPolicyCacheFile reads a last-known-good policy cache. It returns (nil, nil)
// when the file is absent (never applied / invalidated) and an error when it is
// present but unusable (half file, bad JSON, or a different worker's cache) — a
// caller treats any error as "no cache" with a WARN, never a panic.
func ReadPolicyCacheFile(path, workerID string) (*wsproto.Policy, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("policy cache read: %w", err)
	}
	var f policyCacheFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("policy cache decode: %w", err)
	}
	if f.WorkerID != workerID {
		return nil, fmt.Errorf("policy cache worker_id mismatch: file=%q want=%q", f.WorkerID, workerID)
	}
	if f.Policy == nil {
		return nil, fmt.Errorf("policy cache has no policy")
	}
	return f.Policy, nil
}

func revOf(p *wsproto.Policy) int64 {
	if p == nil {
		return 0
	}
	return p.Rev
}

// writePolicyCacheGuarded persists p under cacheMu, but only when seq is still the
// LATEST published apply token (H4): a late retry / a superseded write whose seq no
// longer matches applySeq is dropped (returns nil — it is not a failure, a newer
// apply already owns the cache). A real I/O error is returned so the caller can
// degrade + invalidate + schedule a retry.
//
// G-H1: the cacheMu critical section NEVER takes st.mu / applyMu / updateMu; it only
// reads the atomic token and does file I/O.
func (cl *Client) writePolicyCacheGuarded(p *wsproto.Policy, seq uint64) error {
	if cl.cachePath == "" {
		return nil // no cache configured (LEGACY / tests) — nothing to persist
	}
	cl.cacheMu.Lock()
	defer cl.cacheMu.Unlock()
	if seq != cl.applySeq.Load() {
		return nil // superseded by a newer apply; drop silently
	}
	return cl.writeCacheFn(p, seq)
}

// invalidatePolicyCache removes the on-disk cache so a restart cannot restore a Rev
// older than the one just applied (which would replay revoked permissions). Called
// only after a failed write; best-effort (a missing file is already invalidated).
func (cl *Client) invalidatePolicyCache() {
	if cl.cachePath == "" {
		return
	}
	cl.cacheMu.Lock()
	defer cl.cacheMu.Unlock()
	if err := os.Remove(cl.cachePath); err != nil && !os.IsNotExist(err) {
		slog.Warn("worker could not invalidate policy cache",
			"worker_id", cl.workerID, "path", cl.cachePath, "err", err)
	}
}

// enqueueCacheRetry records the latest failed cache write (latest-wins) and signals
// the retry loop. Safety does not depend on it — it only improves liveness for a
// worker that stays connected long enough that no new apply refreshes the cache.
func (cl *Client) enqueueCacheRetry(p *wsproto.Policy, seq uint64) {
	if cl.cachePath == "" {
		return
	}
	cl.cacheMu.Lock()
	cp := *p
	cl.cacheRetryPending = &pendingCacheWrite{policy: cp, seq: seq}
	cl.cacheMu.Unlock()
	wake(cl.cacheRetryCh)
}

// pendingCacheWrite is one failed cache write awaiting retry (immutable value).
type pendingCacheWrite struct {
	policy wsproto.Policy
	seq    uint64
}

// cacheRetryLoop drains failed cache writes (best effort). It re-attempts the pending
// write when signalled, backing off on continued failure, and drops the pending when
// its seq is superseded by a newer apply. It exits with ctx.
func (cl *Client) cacheRetryLoop(ctx context.Context) {
	if cl.cachePath == "" {
		return
	}
	var timer *time.Timer
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
		}
	}
	for {
		var timerC <-chan time.Time
		if timer != nil {
			timerC = timer.C
		}
		select {
		case <-ctx.Done():
			stopTimer()
			return
		case <-cl.cacheRetryCh:
		case <-timerC:
			timer = nil
		}
		if cl.retryCacheOnce() {
			stopTimer()
			continue
		}
		if timer == nil {
			timer = time.NewTimer(cacheRetryInterval)
		} else {
			timer.Reset(cacheRetryInterval)
		}
	}
}

// retryCacheOnce attempts the pending cache write once; it returns true when nothing
// is left to do (written, superseded, or none pending) and false when a real write
// error means it should be retried later.
func (cl *Client) retryCacheOnce() bool {
	cl.cacheMu.Lock()
	defer cl.cacheMu.Unlock()
	pc := cl.cacheRetryPending
	if pc == nil {
		return true
	}
	if pc.seq != cl.applySeq.Load() {
		cl.cacheRetryPending = nil // a newer apply owns the cache now
		return true
	}
	p := pc.policy
	if err := cl.writeCacheFn(&p, pc.seq); err != nil {
		return false // keep pending, retry after backoff
	}
	cl.cacheRetryPending = nil
	return true
}
