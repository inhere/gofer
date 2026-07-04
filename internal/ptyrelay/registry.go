package ptyrelay

import (
	"errors"
	"sync"
	"time"
)

// RelayState is the serve-side lifecycle state for one pty relay session.
type RelayState string

const (
	RelayPendingWorker RelayState = "pending_worker"
	RelayOpen          RelayState = "open"
	RelayAttached      RelayState = "attached"
	RelayClosing       RelayState = "closing"
	RelayFinalized     RelayState = "finalized"
)

var (
	ErrRelayNotFound     = errors.New("ptyrelay: relay entry not found")
	ErrRelayBadNonce     = errors.New("ptyrelay: relay nonce not found")
	ErrRelayAlreadyOpen  = errors.New("ptyrelay: relay already open")
	ErrRelayFinalized    = errors.New("ptyrelay: relay finalized")
	ErrRelayBindingMatch = errors.New("ptyrelay: relay binding mismatch")
)

// RelayBinding identifies a prepared pty relay session.
type RelayBinding struct {
	WorkerID     string
	InstanceID   string
	JobID        string
	PtySessionID string
	Nonce        string
	Expiry       int64
}

// RelayEntry is one serve-side pty relay record. Callers must treat it as
// read-only and mutate it through Registry methods.
type RelayEntry struct {
	Binding     RelayBinding
	State       RelayState
	Relay       *Relay
	CreatedAt   time.Time
	OpenedAt    time.Time
	ClosedAt    time.Time
	CloseReason string
}

// Registry manages live-only pty relay sessions for serve.
type Registry struct {
	mu        sync.Mutex
	byJob     map[string]*RelayEntry
	bySession map[string]*RelayEntry
	byNonce   map[string]*RelayEntry
	now       func() time.Time
}

// NewRegistry returns an empty live-only relay registry.
func NewRegistry() *Registry {
	return &Registry{
		byJob:     map[string]*RelayEntry{},
		bySession: map[string]*RelayEntry{},
		byNonce:   map[string]*RelayEntry{},
		now:       time.Now,
	}
}

// Prepare creates or replaces a pending_worker relay entry for b. If an older
// entry exists for the same job, it is closed first.
func (r *Registry) Prepare(b RelayBinding) *RelayEntry {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old := r.byJob[b.JobID]; old != nil {
		r.closeLocked(old, "replaced")
		r.removeIndexesLocked(old)
	}
	e := &RelayEntry{
		Binding:   b,
		State:     RelayPendingWorker,
		CreatedAt: r.now(),
	}
	r.byJob[b.JobID] = e
	if b.PtySessionID != "" {
		r.bySession[b.PtySessionID] = e
	}
	if b.Nonce != "" {
		r.byNonce[b.Nonce] = e
	}
	return cloneEntry(e)
}

// Open consumes a prepared nonce and binds the relay source. The returned entry
// is in open state and owns a started Relay.
func (r *Registry) Open(nonce string, source PtySource) (*RelayEntry, error) {
	if r == nil {
		return nil, ErrRelayNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.byNonce[nonce]
	if e == nil {
		return nil, ErrRelayBadNonce
	}
	delete(r.byNonce, nonce)
	switch e.State {
	case RelayPendingWorker:
	case RelayOpen, RelayAttached:
		return nil, ErrRelayAlreadyOpen
	case RelayClosing, RelayFinalized:
		return nil, ErrRelayFinalized
	default:
		return nil, ErrRelayNotFound
	}
	if source == nil {
		return nil, ErrRelayNotFound
	}
	e.Relay = New(source)
	e.Relay.Start()
	e.State = RelayOpen
	e.OpenedAt = r.now()
	return cloneEntry(e), nil
}

// Lookup returns the relay entry for jobID.
func (r *Registry) Lookup(jobID string) (*RelayEntry, bool) {
	if r == nil || jobID == "" {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.byJob[jobID]
	if e == nil {
		return nil, false
	}
	return cloneEntry(e), true
}

// LookupSession returns the relay entry for ptySessionID.
func (r *Registry) LookupSession(ptySessionID string) (*RelayEntry, bool) {
	if r == nil || ptySessionID == "" {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.bySession[ptySessionID]
	if e == nil {
		return nil, false
	}
	return cloneEntry(e), true
}

// MarkAttached records that a browser attach path has claimed the relay. It is
// safe to call repeatedly after the relay is open.
func (r *Registry) MarkAttached(jobID string) (*RelayEntry, error) {
	if r == nil || jobID == "" {
		return nil, ErrRelayNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.byJob[jobID]
	if e == nil {
		return nil, ErrRelayNotFound
	}
	switch e.State {
	case RelayOpen, RelayAttached:
		e.State = RelayAttached
		return cloneEntry(e), nil
	case RelayPendingWorker:
		return nil, ErrRelayNotFound
	case RelayClosing, RelayFinalized:
		return nil, ErrRelayFinalized
	default:
		return nil, ErrRelayNotFound
	}
}

// Close moves a relay to finalized and closes its terminal source. It is
// idempotent; repeated closes for the same job are safe.
func (r *Registry) Close(jobID, reason string) {
	if r == nil || jobID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.byJob[jobID]
	if e == nil {
		return
	}
	r.closeLocked(e, reason)
}

func (r *Registry) closeLocked(e *RelayEntry, reason string) {
	if e.State == RelayFinalized {
		return
	}
	e.State = RelayClosing
	if e.Relay != nil {
		_ = e.Relay.Close()
	}
	e.State = RelayFinalized
	e.ClosedAt = r.now()
	e.CloseReason = reason
	r.removeIndexesLocked(e)
}

func (r *Registry) removeIndexesLocked(e *RelayEntry) {
	delete(r.byJob, e.Binding.JobID)
	delete(r.bySession, e.Binding.PtySessionID)
	delete(r.byNonce, e.Binding.Nonce)
}

func cloneEntry(e *RelayEntry) *RelayEntry {
	if e == nil {
		return nil
	}
	cp := *e
	return &cp
}
