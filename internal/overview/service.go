package overview

import (
	"sync"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// Cache policy (design §关键流程 缓存): an entry is served while it is younger than its
// TTL AND the store's stats generation has not moved since it was built; a moved
// generation still keeps the entry for MinHold, so a burst of ending jobs does not
// rebuild the overview on every poll.
const (
	TTL      = 60 * time.Second
	TTLAll   = 5 * time.Minute
	MinHold  = 10 * time.Second
	maxCache = 64
)

type cacheKey struct {
	rng string
	tz  int
}

type cacheEntry struct {
	ov  *Overview
	gen uint64
	at  time.Time
}

// call is one in-flight build other requests for the same key wait on (single-flight).
type call struct {
	done chan struct{}
	ov   *Overview
	err  error
}

// genSource is the invalidation counter (jobstore.Store.StatsGen).
type genSource interface{ StatsGen() uint64 }

// Service serves cached overviews.
type Service struct {
	store *jobstore.Store
	gen   genSource
	now   func() time.Time
	build func(Query, time.Time) (*Overview, error)

	mu       sync.Mutex
	cache    map[cacheKey]cacheEntry
	inflight map[cacheKey]*call
}

// New builds a Service over the job store.
func New(st *jobstore.Store) *Service {
	s := &Service{store: st, gen: st, now: time.Now,
		cache: map[cacheKey]cacheEntry{}, inflight: map[cacheKey]*call{}}
	s.build = func(q Query, now time.Time) (*Overview, error) { return Build(s.store, q, now) }
	return s
}

// ttlFor: range=all rebuilds every TTLAll; today / 7d / 30d share TTL (the cache key
// carries the range, so each keeps its own entry).
func ttlFor(rng string) time.Duration {
	if rng == RangeAll {
		return TTLAll
	}
	return TTL
}

// fresh reports whether a cached entry may still be served.
func fresh(e cacheEntry, gen uint64, now time.Time, ttl time.Duration) bool {
	age := now.Sub(e.at)
	if age < 0 || age >= ttl {
		return false
	}
	return e.gen == gen || age < MinHold
}

// Get returns the overview for q, from the cache when it is still fresh. A served copy
// carries Cached=true; concurrent misses on one key share a single build.
func (s *Service) Get(q Query) (*Overview, error) {
	q, err := q.Normalize()
	if err != nil {
		return nil, err
	}
	key := cacheKey{q.Range, q.TZMin}
	now := s.now()
	gen := s.gen.StatsGen()

	s.mu.Lock()
	if e, ok := s.cache[key]; ok && fresh(e, gen, now, ttlFor(q.Range)) {
		s.mu.Unlock()
		cp := *e.ov
		cp.Cached = true
		return &cp, nil
	}
	if c, ok := s.inflight[key]; ok {
		s.mu.Unlock()
		<-c.done
		if c.err != nil {
			return nil, c.err
		}
		cp := *c.ov
		cp.Cached = true
		return &cp, nil
	}
	c := &call{done: make(chan struct{})}
	s.inflight[key] = c
	s.mu.Unlock()

	// The generation is read BEFORE the build: a job that ends during it moves the
	// counter past the stored value, so the next request rebuilds.
	c.ov, c.err = s.build(q, now)

	s.mu.Lock()
	delete(s.inflight, key)
	if c.err == nil {
		if len(s.cache) >= maxCache {
			s.cache = map[cacheKey]cacheEntry{} // a handful of tz values in practice
		}
		s.cache[key] = cacheEntry{ov: c.ov, gen: gen, at: now}
	}
	s.mu.Unlock()
	close(c.done)
	return c.ov, c.err
}
