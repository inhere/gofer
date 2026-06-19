package worker

import (
	"math/rand"
	"time"
)

// Reconnect / heartbeat defaults (P3 §4.2 / §4.1). All overridable via the
// worker.yaml server_link.reconnect block; these apply when unset.
const (
	// DefaultInitialBackoff is the first-retry base wait (full-jitter exponent base).
	DefaultInitialBackoff = 1 * time.Second
	// DefaultMaxBackoff caps the backoff so a long hub outage retries at a bounded
	// (≤ this) cadence.
	DefaultMaxBackoff = 30 * time.Second
	// DefaultPingInterval is the worker's heartbeat ping cadence (symmetric with the
	// hub, so a half-open hub is detected by the worker too, §5.1).
	DefaultPingInterval = 15 * time.Second
	// DefaultReadDeadline bounds a single read on the worker side (half-open hub
	// detection). Must be ≥ 2× the ping interval, mirroring the hub invariant.
	DefaultReadDeadline = 45 * time.Second
)

// backoffPolicy holds the resolved full-jitter exponential backoff parameters and
// the random source used to compute the jitter. It is a value type so each Client
// owns its own (a private rand source keeps it -race safe without a global lock).
type backoffPolicy struct {
	initial time.Duration
	max     time.Duration
	rng     *rand.Rand
}

// newBackoffPolicy resolves the policy from initial/max (any non-positive value
// falls to the package default). rng is the jitter source; pass nil for a
// time-seeded source (tests inject a deterministic one).
func newBackoffPolicy(initial, max time.Duration, rng *rand.Rand) backoffPolicy {
	if initial <= 0 {
		initial = DefaultInitialBackoff
	}
	if max <= 0 {
		max = DefaultMaxBackoff
	}
	if max < initial {
		max = initial
	}
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return backoffPolicy{initial: initial, max: max, rng: rng}
}

// next returns the full-jitter backoff for the given retry attempt (0-based):
//
//	cap   = min(max, initial * 2^attempt)
//	sleep = rand[0, cap)
//
// (AWS "full jitter": spreads a thundering herd of workers reconnecting to one
// restarted hub, §6.2.) attempt is clamped so the 2^attempt shift cannot
// overflow; large attempts saturate at max. The result is always in [0, max].
func (b backoffPolicy) next(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	cap := b.max
	// Compute initial * 2^attempt without overflowing: stop doubling once we reach
	// or exceed max (the cap dominates anyway).
	d := b.initial
	for i := 0; i < attempt && d < b.max; i++ {
		d *= 2
	}
	if d < cap {
		cap = d
	}
	if cap <= 0 {
		return 0
	}
	return time.Duration(b.rng.Int63n(int64(cap)))
}
