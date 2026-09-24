package tunnel

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// DefaultForwarderTTL is how long a forwarder registration survives without a
// heartbeat (TUN-03). It is three times the CLI's 30s heartbeat interval, so a single
// dropped heartbeat does not make a live forwarder flicker out of the list.
const DefaultForwarderTTL = 90 * time.Second

// Errors a heartbeat or delete reports for an id the registry does not hold, or holds
// for somebody else.
var (
	// ErrForwarderNotFound: no such registration — never registered here, or expired
	// because its process stopped heartbeating (killed, host asleep, network gone).
	ErrForwarderNotFound = errors.New("forwarder registration not found (expired?)")
	// ErrForwarderNotOwner: the registration belongs to another caller. A caller may
	// only renew or remove its own.
	ErrForwarderNotOwner = errors.New("forwarder registration belongs to another caller")
)

// ForwarderRegistration is one `gofer tun forward` process as the hub knows it: what
// it listens on, where it runs and who registered it.
//
// It is DISPLAY state only. Registering grants no forwarding capability: a connection
// still goes through the ordinary tunnel auth (bearer token, can_tunnel) and the
// worker's own allowlist, exactly as before this registry existed.
type ForwarderRegistration struct {
	// ID is hub-assigned ("fw-<8hex>"); a client never chooses it.
	ID string
	// CallerID is the authenticated caller that registered it, recorded by the hub —
	// a client cannot claim another identity, nor touch another caller's entry.
	CallerID string
	Worker   string
	Specs    []ForwardSpec
	Host     string
	PID      int
	// StartedAt is when the forwarder started listening (the client's own clock).
	StartedAt time.Time
	// LastSeenAt is stamped by Register and refreshed by every heartbeat; the TTL
	// counts from it, which is what keeps the uptime and the liveness answer separate.
	LastSeenAt time.Time
}

// ForwarderRegistry is the hub-side, in-memory registry of live forwarder processes.
//
// TTL is read through a func, never cached: the value comes from
// server.tunnel.forwarder_ttl_sec, so a config reload applies to the next read without
// rebuilding the registry. Expiry is evaluated on every read and write, so no sweeper
// goroutine (and no shutdown ordering) is needed.
type ForwarderRegistry struct {
	mu    sync.Mutex
	ttl   func() time.Duration
	now   func() time.Time
	items map[string]ForwarderRegistration
}

// NewForwarderRegistry builds a registry. ttl may be nil (or return <= 0), which means
// DefaultForwarderTTL.
func NewForwarderRegistry(ttl func() time.Duration) *ForwarderRegistry {
	return &ForwarderRegistry{ttl: ttl, now: time.Now, items: map[string]ForwarderRegistration{}}
}

// TTL is the effective registration lifetime: the configured value, else
// DefaultForwarderTTL.
func (r *ForwarderRegistry) TTL() time.Duration {
	if r.ttl != nil {
		if d := r.ttl(); d > 0 {
			return d
		}
	}
	return DefaultForwarderTTL
}

// Register stores a new registration and returns it with its hub-assigned id and
// caller. StartedAt defaults to now when the client did not send one.
func (r *ForwarderRegistry) Register(caller string, reg ForwarderRegistration) ForwarderRegistration {
	now := r.now()
	if reg.StartedAt.IsZero() {
		reg.StartedAt = now
	}
	reg.ID = newForwarderID()
	reg.CallerID = caller
	reg.LastSeenAt = now
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked(now)
	r.items[reg.ID] = reg
	return reg
}

// Heartbeat refreshes a registration and, when specs are given, replaces its rule list
// (the forwarder may have been restarted with different ports under the same id — the
// CLI re-registers in that case, but a heartbeat carrying rules keeps both ends
// honest). Callers may only renew their own.
func (r *ForwarderRegistry) Heartbeat(id, caller string, specs []ForwardSpec) (ForwarderRegistration, error) {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked(now)
	reg, ok := r.items[id]
	if !ok {
		return ForwarderRegistration{}, ErrForwarderNotFound
	}
	if reg.CallerID != caller {
		return ForwarderRegistration{}, ErrForwarderNotOwner
	}
	if len(specs) > 0 {
		reg.Specs = specs
	}
	reg.LastSeenAt = now
	r.items[id] = reg
	return reg, nil
}

// Delete removes a registration (the forwarder's clean goodbye). An unknown id is
// ErrForwarderNotFound and another caller's id is ErrForwarderNotOwner.
func (r *ForwarderRegistry) Delete(id, caller string) error {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked(now)
	reg, ok := r.items[id]
	if !ok {
		return ErrForwarderNotFound
	}
	if reg.CallerID != caller {
		return ErrForwarderNotOwner
	}
	delete(r.items, id)
	return nil
}

// List returns the live registrations, oldest first, after dropping the ones whose
// heartbeat is older than the TTL.
func (r *ForwarderRegistry) List() []ForwarderRegistration {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked(now)
	out := make([]ForwarderRegistration, 0, len(r.items))
	for _, reg := range r.items {
		out = append(out, reg)
	}
	// Stable order: a registry map iterates randomly, and a list that reorders itself
	// between two polls is unreadable on the console.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// sweepLocked drops every registration silent for longer than the TTL. The caller
// holds r.mu.
func (r *ForwarderRegistry) sweepLocked(now time.Time) {
	ttl := r.TTL()
	for id, reg := range r.items {
		if now.Sub(reg.LastSeenAt) > ttl {
			delete(r.items, id)
		}
	}
}

// newForwarderID mints "fw-<8hex>". Like the tunnel ids, it comes from crypto/rand: a
// guessable id would let one caller heartbeat or delete another's registration.
func newForwarderID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("tunnel: forwarder id: %w", err))
	}
	return "fw-" + hex.EncodeToString(b)
}
