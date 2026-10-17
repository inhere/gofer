package workerenroll

import (
	"container/list"
	"sync"
	"time"
)

// Limits of the in-memory source table (design 「默认数值」: 4096 sources, LRU + TTL).
const (
	MaxLimiterSources = 4096
	limiterWindow     = time.Minute
)

// SourceLimiter implements Limiter and SourceBlocker in memory: a sliding one-minute
// request window per source plus a block table. Entries are evicted when they carry
// no live information (window empty and block lapsed) or, past the cap, by LRU.
type SourceLimiter struct {
	mu     sync.Mutex
	perMin func() int
	max    int
	clock  func() time.Time
	byKey  map[string]*list.Element // value: *sourceEntry
	lru    *list.List               // front = most recently used
	nextGC time.Time
}

type sourceEntry struct {
	source       string
	hits         []time.Time
	blockedUntil time.Time
}

// NewSourceLimiter builds a limiter; perMin is read on every call (hot reload) and a
// value <= 0 means the default of 3. clock may be nil.
func NewSourceLimiter(perMin func() int, clock func() time.Time) *SourceLimiter {
	if clock == nil {
		clock = time.Now
	}
	return &SourceLimiter{perMin: perMin, max: MaxLimiterSources, clock: clock,
		byKey: map[string]*list.Element{}, lru: list.New()}
}

func (l *SourceLimiter) rate() int {
	if l.perMin != nil {
		if n := l.perMin(); n > 0 {
			return n
		}
	}
	return 3
}

// Allow records a request from source and returns ErrSourceBlocked while the source
// is blocked or ErrRateLimited when it already made its quota inside the last minute.
func (l *SourceLimiter) Allow(source string, now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gc(now)
	e := l.touch(source, now)
	if now.Before(e.blockedUntil) {
		return ErrSourceBlocked
	}
	cut := now.Add(-limiterWindow)
	live := e.hits[:0]
	for _, h := range e.hits {
		if h.After(cut) {
			live = append(live, h)
		}
	}
	e.hits = live
	if len(e.hits) >= l.rate() {
		return ErrRateLimited
	}
	e.hits = append(e.hits, now)
	return nil
}

// Block refuses source for d. A non-positive d is ignored.
func (l *SourceLimiter) Block(source string, d time.Duration) {
	if d <= 0 {
		return
	}
	now := l.clock()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.touch(source, now).blockedUntil = now.Add(d)
}

// Len reports how many sources the table holds (tests / diagnostics).
func (l *SourceLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.byKey)
}

// touch returns the entry for source (creating it, evicting if over the cap) and
// marks it most recently used. Callers hold l.mu.
func (l *SourceLimiter) touch(source string, now time.Time) *sourceEntry {
	if el, ok := l.byKey[source]; ok {
		l.lru.MoveToFront(el)
		return el.Value.(*sourceEntry)
	}
	for len(l.byKey) >= l.max {
		l.evictOne(now)
	}
	e := &sourceEntry{source: source}
	l.byKey[source] = l.lru.PushFront(e)
	return e
}

// evictOne drops the least recently used entry, preferring one with no active block
// so a flood of new sources cannot flush the block table. Callers hold l.mu.
func (l *SourceLimiter) evictOne(now time.Time) {
	var victim *list.Element
	for el := l.lru.Back(); el != nil; el = el.Prev() {
		if !now.Before(el.Value.(*sourceEntry).blockedUntil) {
			victim = el
			break
		}
	}
	if victim == nil {
		victim = l.lru.Back()
	}
	if victim == nil {
		return
	}
	l.lru.Remove(victim)
	delete(l.byKey, victim.Value.(*sourceEntry).source)
}

// gc removes entries whose window is empty and whose block lapsed (the TTL). It runs
// at most once per window so Allow stays cheap. Callers hold l.mu.
func (l *SourceLimiter) gc(now time.Time) {
	if now.Before(l.nextGC) {
		return
	}
	l.nextGC = now.Add(limiterWindow / 2)
	cut := now.Add(-limiterWindow)
	for el := l.lru.Back(); el != nil; {
		prev := el.Prev()
		e := el.Value.(*sourceEntry)
		idle := !now.Before(e.blockedUntil) && (len(e.hits) == 0 || !e.hits[len(e.hits)-1].After(cut))
		if idle {
			l.lru.Remove(el)
			delete(l.byKey, e.source)
		}
		el = prev
	}
}
