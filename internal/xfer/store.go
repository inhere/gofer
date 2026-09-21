// Package xfer implements XFER-01's transfer staging area and journal: the
// on-disk staging directory a transfer's payload passes through
// (<root>/xfer/<id>/data) and the business operations recorded against it (stage
// a put/get, dispatch it to the executing machine, settle it done/failed, expire
// it). It is a data/business layer package: it never imports httpapi/commands
// (G022) — the CLI, the HTTP handlers and the worker hub all consume it through
// the seams declared here.
//
// The payload bytes NEVER travel through the journal: the HTTP layer streams them
// into Store (a temporary file renamed into place only after the sha256 and size
// have been verified), so an aborted or corrupted upload can never be handed to
// an executing machine.
package xfer

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Op is a transfer's direction. put = a local file travels TO the executing
// machine; get = a file is fetched FROM it.
type Op string

const (
	OpPut Op = "put"
	OpGet Op = "get"
)

// State is a transfer's position in the state machine (design §一.2). staged =
// the payload is complete on the server (put) or the record is ready to dispatch
// (get); dispatched = handed to the executing machine, awaiting its result; done
// = the bytes are where they belong; failed/expired are terminal.
type State string

const (
	StateStaged     State = "staged"
	StateDispatched State = "dispatched"
	StateDone       State = "done"
	StateFailed     State = "failed"
	StateExpired    State = "expired"
)

// Terminal reports whether a state can no longer change.
func (s State) Terminal() bool {
	return s == StateDone || s == StateFailed || s == StateExpired
}

// Sentinel errors. ErrTooLarge and ErrExists are surfaced to the caller's wire
// error text verbatim (the CLI prints them as-is), so their messages are part of
// the contract.
var (
	// ErrTooLarge is returned when a transfer exceeds the configured byte cap.
	ErrTooLarge = errors.New("too large")
	// ErrNotFound is returned when no journal row matches an id.
	ErrNotFound = errors.New("xfer not found")
	// ErrExists is the worker-side refusal to overwrite an existing destination
	// without --force (mapped to a failed transfer with error "exists").
	ErrExists = errors.New("exists")
	// ErrNotStaged is returned when an operation requires a staged record.
	ErrNotStaged = errors.New("xfer is not staged")
)

// Limits are the operator-tunable caps (server.xfer): the per-transfer byte
// ceiling, how long a staged/finished transfer survives in the staging area, and
// how much one JOB's `--collect` may bring back in total (XFER-01 X2).
type Limits struct {
	MaxBytes int64
	TTL      time.Duration
	// CollectMaxBytes caps the sum of one job's collected files. Per-file is still
	// MaxBytes; this is what stops a job from filling the disk with many files that
	// are each under the cap.
	CollectMaxBytes int64
}

// DefaultLimits are the documented XFER-01 defaults (design §一.1/§一.3): 256MB per
// transfer, 24h in the staging area, 1GB per job's collect.
func DefaultLimits() Limits {
	return Limits{MaxBytes: 256 << 20, TTL: 24 * time.Hour, CollectMaxBytes: 1 << 30}
}

// Store owns the staging area: one directory per transfer id under root,
// containing the payload as `data` plus `data.tmp` while a transfer is being
// written. All payload I/O goes through here so the temp-then-rename discipline
// (and the TTL sweep) lives in exactly one place.
type Store struct {
	root string
}

// NewStore returns a staging store rooted at root (the `xfer` directory itself —
// the Manager composes it from the configured storage/config root).
func NewStore(root string) *Store { return &Store{root: root} }

// Root is the directory every transfer directory lives under.
func (s *Store) Root() string { return s.root }

// Dir is a transfer's staging directory.
func (s *Store) Dir(id string) string { return filepath.Join(s.root, id) }

// Path is the absolute path of a transfer's payload.
func (s *Store) Path(id string) string { return filepath.Join(s.root, id, "data") }

func (s *Store) tmpPath(id string) string { return filepath.Join(s.root, id, "data.tmp") }

// Create makes (idempotently) a transfer's staging directory.
func (s *Store) Create(id string) error {
	if err := os.MkdirAll(s.Dir(id), 0o700); err != nil {
		return fmt.Errorf("xfer: create staging %s: %w", id, err)
	}
	return nil
}

// Writer opens the temporary payload file for a transfer. The caller MUST close
// it and then call Finalize (or Remove) — an un-finalized temporary file is never
// served.
func (s *Store) Writer(id string) (io.WriteCloser, error) {
	if err := s.Create(id); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.tmpPath(id), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("xfer: open staging temp %s: %w", id, err)
	}
	return f, nil
}

// Finalize moves the verified payload into place. size < 0 skips the size check.
// The caller has already verified the sha256 against the expected value while
// streaming (that is what makes an aborted upload impossible to serve); Finalize
// only guards against a truncated/oversized temp file and makes the payload
// visible atomically.
func (s *Store) Finalize(id string, size int64) error {
	tmp := s.tmpPath(id)
	st, err := os.Stat(tmp)
	if err != nil {
		return fmt.Errorf("xfer: finalize %s: %w", id, err)
	}
	if size >= 0 && st.Size() != size {
		return fmt.Errorf("xfer: finalize %s: staged size %d != expected %d", id, st.Size(), size)
	}
	if err := os.Rename(tmp, s.Path(id)); err != nil {
		return fmt.Errorf("xfer: finalize %s: %w", id, err)
	}
	return nil
}

// Reader opens a transfer's payload for reading (downloading to a client, or to
// an executing machine).
func (s *Store) Reader(id string) (io.ReadCloser, error) {
	f, err := os.Open(s.Path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: staged payload %s", ErrNotFound, id)
		}
		return nil, fmt.Errorf("xfer: open staging %s: %w", id, err)
	}
	return f, nil
}

// Stat reports the payload's size (an error when it is not present).
func (s *Store) Stat(id string) (int64, error) {
	st, err := os.Stat(s.Path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("%w: staged payload %s", ErrNotFound, id)
		}
		return 0, fmt.Errorf("xfer: stat staging %s: %w", id, err)
	}
	return st.Size(), nil
}

// Remove deletes a transfer's whole staging directory. It is not an error when
// the directory is already gone.
func (s *Store) Remove(id string) error {
	if err := os.RemoveAll(s.Dir(id)); err != nil {
		return fmt.Errorf("xfer: remove staging %s: %w", id, err)
	}
	return nil
}

// Sweep removes staging directories whose last modification is older than ttl —
// the disk-side half of expiry, covering the orphans the journal no longer knows
// about (a crash between Create and Insert). It returns the ids it removed, so a
// caller can log them.
func (s *Store) Sweep(now time.Time, ttl time.Duration) ([]string, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("xfer: sweep %s: %w", s.root, err)
	}
	var removed []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // vanished under us: nothing to clean
		}
		if now.Sub(info.ModTime()) < ttl {
			continue
		}
		if err := s.Remove(e.Name()); err != nil {
			return removed, err
		}
		removed = append(removed, e.Name())
	}
	return removed, nil
}

// NewID mints a transfer id (128-bit random hex). It is unguessable on purpose:
// the id is the only thing an executing worker needs to fetch a payload, so it
// must not be enumerable.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails in practice; a time-based fallback keeps the
		// process alive rather than panicking on a theoretical syscall error.
		return fmt.Sprintf("x%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
