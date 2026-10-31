package trigger

import (
	"math"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// Limit is ONE bucket a request must pass: a token bucket (RatePerSec refills, Burst
// capacity) plus an in-flight cap. A zero RatePerSec means "no rate gating" and a zero
// MaxInflight "no in-flight cap". Key names the bucket, so two limits with the same key
// share state (the owner caller's limit is shared by all its triggers).
type Limit struct {
	Key         string
	RatePerSec  float64
	Burst       int
	MaxInflight int
}

// Decision is the verdict of Limiter.Acquire.
type Decision struct {
	OK bool
	// RetryAfter is how long until the refused request could pass (token refill time, or
	// a short fixed hint when the in-flight cap is what refused it). It is what a Retry-After
	// header is made of; zero when OK.
	RetryAfter time.Duration
}

type bucket struct {
	tokens   float64
	last     time.Time
	inflight int
	seen     bool
}

// Limiter is an in-process token-bucket limiter with an injected clock. The state is
// deliberately not persisted (design 7a: memory limiter + durable idempotency).
type Limiter struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[string]*bucket
}

// inflightRetryHint is the Retry-After offered when only the in-flight cap refused: there
// is no refill time to compute, a finished request frees the slot at an unknown moment.
const inflightRetryHint = time.Second

// NewLimiter builds a limiter that reads time from now (time.Now when nil).
func NewLimiter(now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{now: now, buckets: make(map[string]*bucket)}
}

// Acquire passes a request through every limit atomically: it is admitted only if ALL
// limits admit it, and then consumes one token and one in-flight slot from each; a refusal
// consumes nothing. The returned release must be called when the request is done (it is
// idempotent and a no-op for a refused request).
//
// When several limits refuse, RetryAfter is the longest wait, since the request needs
// every one of them to open.
func (l *Limiter) Acquire(limits ...Limit) (release func(), d Decision) {
	noop := func() {}
	if l == nil {
		return noop, Decision{OK: true}
	}
	active := make([]Limit, 0, len(limits))
	for _, lim := range limits {
		if lim.RatePerSec > 0 || lim.MaxInflight > 0 {
			active = append(active, lim)
		}
	}
	if len(active) == 0 {
		return noop, Decision{OK: true}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var wait time.Duration
	refused := false
	for _, lim := range active {
		b := l.refill(lim, now)
		if lim.RatePerSec > 0 && b.tokens < 1 {
			refused = true
			if w := time.Duration(math.Ceil((1 - b.tokens) / lim.RatePerSec * float64(time.Second))); w > wait {
				wait = w
			}
		}
		if lim.MaxInflight > 0 && b.inflight >= lim.MaxInflight {
			refused = true
			if inflightRetryHint > wait {
				wait = inflightRetryHint
			}
		}
	}
	if refused {
		return noop, Decision{RetryAfter: wait}
	}
	for _, lim := range active {
		b := l.buckets[lim.Key]
		if lim.RatePerSec > 0 {
			b.tokens--
		}
		b.inflight++
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			for _, lim := range active {
				if b := l.buckets[lim.Key]; b != nil && b.inflight > 0 {
					b.inflight--
				}
			}
		})
	}, Decision{OK: true}
}

// refill brings the bucket of lim up to now and returns it. A bucket starts full. The
// capacity follows the CURRENT limit, so a reload that lowers the burst takes effect at
// once.
func (l *Limiter) refill(lim Limit, now time.Time) *bucket {
	b := l.buckets[lim.Key]
	capacity := float64(lim.Burst)
	if capacity < 1 {
		capacity = math.Max(1, math.Ceil(lim.RatePerSec))
	}
	if b == nil {
		b = &bucket{}
		l.buckets[lim.Key] = b
	}
	if !b.seen {
		b.seen = true
		b.tokens = capacity
		b.last = now
		return b
	}
	if lim.RatePerSec > 0 {
		if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
			b.tokens += elapsed * lim.RatePerSec
		}
		if b.tokens > capacity {
			b.tokens = capacity
		}
	}
	if now.After(b.last) {
		b.last = now
	}
	return b
}

// EffectiveLimits is the set of limits a request to tc must pass: the trigger's own
// (per_minute with burst, max_inflight) and the owner caller's (rate_limit / rate_burst and
// max_concurrent_jobs, with the governance defaults), each in its own bucket. Acquire
// admits only when every one admits, which is the "stricter of the two" rule of design
// 3.5-4: a trigger can lower its own rate below the owner's, never raise it above.
//
// The owner's bucket is shared by all of its triggers.
func EffectiveLimits(tc config.TriggerConfig, sc *config.ServerConfig) []Limit {
	own := Limit{Key: "trigger:" + tc.Name, MaxInflight: tc.Rate.MaxInflight}
	if tc.Rate.PerMinute > 0 {
		own.RatePerSec = float64(tc.Rate.PerMinute) / 60
		// Without an explicit burst the bucket holds one minute of quota, which is the
		// shape the old per-minute window had.
		own.Burst = tc.Rate.Burst
		if own.Burst <= 0 {
			own.Burst = tc.Rate.PerMinute
		}
	}
	out := []Limit{own}
	if sc != nil {
		owner := Limit{Key: "owner:" + tc.OwnerCaller, MaxInflight: sc.CallerConcurrencyLimit(tc.OwnerCaller)}
		owner.RatePerSec, owner.Burst = sc.CallerRate(tc.OwnerCaller)
		out = append(out, owner)
	}
	return out
}
