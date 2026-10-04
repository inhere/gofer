// Package pushhub is the in-process fan-out behind the browser's single /v1/ws
// connection (Q2). Publishers (job persist, the event tap, decision/session writes,
// worker presence) only ever call the non-blocking Notify* methods; the hub turns
// those into frames on per-connection bounded queues. A queue that fills drops the
// increment and flags the connection for a `resync`, so a slow browser can never stall
// recordEvent or persist.
//
// The package is a leaf: it knows topics, frames and queues, nothing about jobs,
// stores or HTTP (G022).
package pushhub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Topics. A `job:<id>` topic carries one job's increments; the rest are global.
const (
	TopicStats     = "stats"
	TopicPending   = "pending"
	TopicJobs      = "jobs"
	TopicSessions  = "sessions"
	TopicRunners   = "runners"
	TopicMeta      = "meta"
	TopicPlans     = "plans"
	TopicWorkflows = "workflows"
	TopicSchedules = "schedules"

	JobTopicPrefix = "job:"
)

var globalTopics = map[string]bool{
	TopicStats: true, TopicPending: true, TopicJobs: true, TopicSessions: true,
	TopicRunners: true, TopicMeta: true, TopicPlans: true, TopicWorkflows: true, TopicSchedules: true,
}

// JobTopic names one job's topic.
func JobTopic(id string) string { return JobTopicPrefix + id }

// ValidTopic reports whether a client may subscribe to topic.
func ValidTopic(topic string) bool {
	if globalTopics[topic] {
		return true
	}
	return strings.HasPrefix(topic, JobTopicPrefix) && len(topic) > len(JobTopicPrefix) && len(topic) <= 128
}

// Defaults (the design's numbers).
const (
	DefaultQueueSize         = 64
	DefaultMaxConnsPerCaller = 16
	// DefaultStatsInterval: stats is heavy (two 200ms-budget passes), so it is
	// coalesced to at most one computation per interval, and only with subscribers.
	DefaultStatsInterval = 2 * time.Second
	// DefaultSessionsInterval: session churn (turn counters) is throttled the same way.
	DefaultSessionsInterval = 2 * time.Second
	// DefaultInvalInterval spaces the cheap invalidations (jobs, runners, ...) so a burst
	// of writes reaches the browser as one refetch trigger.
	DefaultInvalInterval = 300 * time.Millisecond
	// DefaultPendingInterval keeps the bell snappy while still merging a burst.
	DefaultPendingInterval = 200 * time.Millisecond

	maxTopicsPerConn  = 64
	maxBackfillEvents = 500
	maxJobsInvalIDs   = 50
)

// ErrTooManyConns is Register's refusal once a caller already holds the maximum.
var ErrTooManyConns = errors.New("pushhub: too many connections for this caller")

// ErrClosed is returned by Conn.Next once the connection or hub is closed.
var ErrClosed = errors.New("pushhub: connection closed")

// SnapFunc computes one snapshot topic's payload. It runs on the hub's own
// goroutines (never on a publisher's), and may be slow.
type SnapFunc func() (any, error)

// JobEvent is one lifecycle event as backfilled for a `job:<id>` subscription.
type JobEvent struct {
	Seq    int64  `json:"seq"`
	Type   string `json:"type"`
	Detail string `json:"detail,omitempty"`
	At     int64  `json:"at"`
}

// BackfillFunc returns the events of one job after a sequence cursor.
type BackfillFunc func(jobID string, sinceSeq int64) ([]JobEvent, error)

// Options tunes a Hub; zero values take the defaults above.
type Options struct {
	QueueSize         int
	MaxConnsPerCaller int
	StatsInterval     time.Duration
	SessionsInterval  time.Duration
	InvalInterval     time.Duration
	PendingInterval   time.Duration
	// Version is stamped on the hello frame; a changed value tells the page a new build
	// is running.
	Version func() string
	Now     func() time.Time
}

// Hub is the subscription table plus the coalescers that feed it.
type Hub struct {
	opts Options

	mu        sync.RWMutex
	conns     map[*Conn]struct{}
	byTopic   map[string]map[*Conn]struct{}
	perCaller map[string]int
	closed    bool

	snapMu   sync.Mutex
	snapFns  map[string]SnapFunc
	snapLast map[string][]byte

	backfill atomic.Pointer[BackfillFunc]

	co map[string]*coalescer

	dropped atomic.Int64
	resyncs atomic.Int64
}

// New builds a Hub.
func New(o Options) *Hub {
	if o.QueueSize <= 0 {
		o.QueueSize = DefaultQueueSize
	}
	if o.MaxConnsPerCaller <= 0 {
		o.MaxConnsPerCaller = DefaultMaxConnsPerCaller
	}
	if o.StatsInterval <= 0 {
		o.StatsInterval = DefaultStatsInterval
	}
	if o.SessionsInterval <= 0 {
		o.SessionsInterval = DefaultSessionsInterval
	}
	if o.InvalInterval <= 0 {
		o.InvalInterval = DefaultInvalInterval
	}
	if o.PendingInterval <= 0 {
		o.PendingInterval = DefaultPendingInterval
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	h := &Hub{
		opts:      o,
		conns:     map[*Conn]struct{}{},
		byTopic:   map[string]map[*Conn]struct{}{},
		perCaller: map[string]int{},
		snapFns:   map[string]SnapFunc{},
		snapLast:  map[string][]byte{},
		co:        map[string]*coalescer{},
	}
	intervals := map[string]time.Duration{
		TopicStats:     o.StatsInterval,
		TopicSessions:  o.SessionsInterval,
		TopicPending:   o.PendingInterval,
		TopicJobs:      o.InvalInterval,
		TopicRunners:   o.InvalInterval,
		TopicMeta:      o.InvalInterval,
		TopicPlans:     o.InvalInterval,
		TopicWorkflows: o.InvalInterval,
		TopicSchedules: o.InvalInterval,
	}
	for topic, iv := range intervals {
		h.co[topic] = &coalescer{h: h, topic: topic, interval: iv}
	}
	return h
}

// SetSnapshot installs the payload source of a snapshot topic (stats, pending).
func (h *Hub) SetSnapshot(topic string, fn SnapFunc) {
	h.snapMu.Lock()
	h.snapFns[topic] = fn
	h.snapMu.Unlock()
}

// SetBackfill installs the `job:<id>` since_seq source.
func (h *Hub) SetBackfill(fn BackfillFunc) {
	if fn == nil {
		h.backfill.Store(nil)
		return
	}
	h.backfill.Store(&fn)
}

// Stats reports the drop and resync counters (tests and diagnostics).
func (h *Hub) Stats() (dropped, resyncs int64) { return h.dropped.Load(), h.resyncs.Load() }

// ConnCount is the number of live connections.
func (h *Hub) ConnCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

// Subscribers is the number of connections subscribed to topic.
func (h *Hub) Subscribers(topic string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byTopic[topic])
}

// Register admits a new connection for caller. The hello frame is already queued.
func (h *Hub) Register(caller string) (*Conn, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, ErrClosed
	}
	if h.perCaller[caller] >= h.opts.MaxConnsPerCaller {
		h.mu.Unlock()
		return nil, ErrTooManyConns
	}
	c := &Conn{
		h:      h,
		caller: caller,
		send:   make(chan []byte, h.opts.QueueSize),
		kick:   make(chan struct{}, 1),
		done:   make(chan struct{}),
		topics: map[string]struct{}{},
	}
	h.conns[c] = struct{}{}
	h.perCaller[caller]++
	h.mu.Unlock()

	ver := ""
	if h.opts.Version != nil {
		ver = h.opts.Version()
	}
	c.push(frame("hello", "", map[string]any{"server_time": h.opts.Now().UnixMilli(), "version": ver}))
	return c, nil
}

// CloseAll drops every connection and refuses new ones (server shutdown).
func (h *Hub) CloseAll() {
	h.mu.Lock()
	h.closed = true
	conns := make([]*Conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

func (h *Hub) remove(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.conns[c]; !ok {
		return
	}
	delete(h.conns, c)
	if n := h.perCaller[c.caller] - 1; n > 0 {
		h.perCaller[c.caller] = n
	} else {
		delete(h.perCaller, c.caller)
	}
	for topic := range c.topics {
		h.dropSubLocked(topic, c)
	}
	c.topics = map[string]struct{}{}
}

func (h *Hub) dropSubLocked(topic string, c *Conn) {
	set := h.byTopic[topic]
	delete(set, c)
	if len(set) == 0 {
		delete(h.byTopic, topic)
		h.snapMu.Lock()
		delete(h.snapLast, topic) // nobody is keeping it fresh any more
		h.snapMu.Unlock()
	}
}

// subscribersOf copies the current subscriber set of a topic.
func (h *Hub) subscribersOf(topic string) []*Conn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	set := h.byTopic[topic]
	if len(set) == 0 {
		return nil
	}
	out := make([]*Conn, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	return out
}

// broadcast queues one pre-built frame to every subscriber of topic, never blocking.
func (h *Hub) broadcast(topic string, b []byte) {
	for _, c := range h.subscribersOf(topic) {
		c.push(b)
	}
}

// ---------------------------------------------------------------- publishers

// Notify marks a global topic changed. Snapshot topics (stats, pending) are
// recomputed, the rest get an `inval`; either way bursts are coalesced and the work
// happens on a hub goroutine, so Notify is safe on any hot path.
func (h *Hub) Notify(topic string) {
	if co := h.co[topic]; co != nil {
		co.poke("", "")
	}
}

// NotifyJob reports that job id changed to status: it feeds the `jobs` list
// invalidation (with the id), the `job:<id>` topic, and stats.
func (h *Hub) NotifyJob(id, status string) {
	if co := h.co[TopicJobs]; co != nil {
		co.poke(id, status)
	}
	if h.Subscribers(JobTopic(id)) > 0 {
		h.broadcast(JobTopic(id), frame("evt", JobTopic(id), map[string]any{"kind": "status", "id": id, "status": status}))
	}
	h.Notify(TopicStats)
}

// PublishJobEvent forwards one recorded event to the job's subscribers. It is a no-op
// (and allocation-free) when nobody watches the job.
func (h *Hub) PublishJobEvent(id string, ev JobEvent) {
	topic := JobTopic(id)
	if h.Subscribers(topic) == 0 {
		return
	}
	h.broadcast(topic, frame("evt", topic, map[string]any{
		"kind": "event", "seq": ev.Seq, "type": ev.Type, "detail": ev.Detail, "at": ev.At,
	}))
}

// PublishJobKind sends a typed hint (e.g. "interaction") to a job's subscribers.
func (h *Hub) PublishJobKind(id, kind string) {
	topic := JobTopic(id)
	if h.Subscribers(topic) == 0 {
		return
	}
	h.broadcast(topic, frame("evt", topic, map[string]any{"kind": kind, "id": id}))
}

// NoteEvent routes one recorded event (any scope) onto the topics it affects. seq is
// the event's row sequence, scope the job id or a synthetic `plan:<id>`-style scope.
func (h *Hub) NoteEvent(scope string, ev JobEvent) {
	switch {
	case strings.HasPrefix(scope, "plan:"):
		h.Notify(TopicPlans)
	case strings.HasPrefix(scope, "agent:"), scope == "config":
		h.Notify(TopicMeta)
	case strings.HasPrefix(scope, "xfer:"), strings.HasPrefix(scope, "session:"):
		// no browser topic
	default:
		h.PublishJobEvent(scope, ev)
	}
	t := ev.Type
	switch {
	case strings.HasPrefix(t, "interaction."):
		h.Notify(TopicPending)
		h.Notify(TopicStats)
	case strings.HasPrefix(t, "plan."):
		h.Notify(TopicPlans)
	case strings.HasPrefix(t, "workflow."), strings.HasPrefix(t, "step."), strings.HasPrefix(t, "subworkflow."):
		h.Notify(TopicWorkflows)
	case strings.HasPrefix(t, "schedule."):
		h.Notify(TopicSchedules)
		h.Notify(TopicStats)
	case t == "config.updated", t == "agent.degraded", t == "agent.recovered":
		h.Notify(TopicMeta)
	}
}

// ---------------------------------------------------------------- snapshots

func (h *Hub) snapFn(topic string) SnapFunc {
	h.snapMu.Lock()
	defer h.snapMu.Unlock()
	return h.snapFns[topic]
}

// compute builds and caches the framed snapshot of topic.
func (h *Hub) compute(topic string) ([]byte, bool) {
	fn := h.snapFn(topic)
	if fn == nil {
		return nil, false
	}
	data, err := fn()
	if err != nil {
		return nil, false
	}
	b := frame("snap", topic, data)
	h.snapMu.Lock()
	h.snapLast[topic] = b
	h.snapMu.Unlock()
	return b, true
}

// initialSnap is what a new subscriber gets: the cached frame when somebody is
// already keeping the topic fresh, else a fresh computation.
func (h *Hub) initialSnap(topic string) ([]byte, bool) {
	h.snapMu.Lock()
	b, ok := h.snapLast[topic]
	h.snapMu.Unlock()
	if ok {
		return b, true
	}
	return h.compute(topic)
}

// ---------------------------------------------------------------- coalescer

// coalescer merges a burst of pokes into one emission: the first poke fires at once
// (leading edge), later ones inside the interval share one trailing emission.
type coalescer struct {
	h        *Hub
	topic    string
	interval time.Duration

	mu      sync.Mutex
	last    time.Time
	timer   *time.Timer
	running bool
	dirty   bool
	ids     map[string]string
	allIDs  bool
}

func (c *coalescer) poke(id, status string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id != "" {
		if c.ids == nil {
			c.ids = map[string]string{}
		}
		if len(c.ids) >= maxJobsInvalIDs {
			c.allIDs = true
		} else {
			c.ids[id] = status
		}
	}
	if c.timer != nil || c.running {
		c.dirty = true
		return
	}
	c.scheduleLocked()
}

func (c *coalescer) scheduleLocked() {
	delay := c.interval - c.h.opts.Now().Sub(c.last)
	if delay < 0 {
		delay = 0
	}
	c.timer = time.AfterFunc(delay, c.fire)
}

func (c *coalescer) fire() {
	c.mu.Lock()
	c.timer = nil
	c.running = true
	c.dirty = false
	c.last = c.h.opts.Now()
	ids, all := c.ids, c.allIDs
	c.ids, c.allIDs = nil, false
	c.mu.Unlock()

	c.h.emit(c.topic, ids, all)

	c.mu.Lock()
	c.running = false
	if c.dirty && c.timer == nil {
		c.scheduleLocked()
	}
	c.mu.Unlock()
}

// emit sends one coalesced notification: a recomputed snapshot for snapshot topics,
// otherwise an inval. Without subscribers it does nothing (stats is not computed for
// an empty room).
func (h *Hub) emit(topic string, ids map[string]string, allIDs bool) {
	if h.Subscribers(topic) == 0 {
		return
	}
	if h.snapFn(topic) != nil {
		if b, ok := h.compute(topic); ok {
			h.broadcast(topic, b)
		}
		return
	}
	data := map[string]any{}
	if topic == TopicJobs {
		if allIDs {
			data["all"] = true
		} else if len(ids) > 0 {
			list := make([]map[string]string, 0, len(ids))
			for id, st := range ids {
				list = append(list, map[string]string{"id": id, "status": st})
			}
			data["jobs"] = list
		}
	}
	h.broadcast(topic, frame("inval", topic, data))
}

// ---------------------------------------------------------------- conn

// Conn is one browser connection: a bounded outbound queue plus its subscriptions.
type Conn struct {
	h      *Hub
	caller string
	send   chan []byte
	kick   chan struct{}
	done   chan struct{}
	once   sync.Once

	overflow atomic.Bool
	topics   map[string]struct{} // guarded by h.mu
}

// Caller is the authenticated identity the connection was registered under.
func (c *Conn) Caller() string { return c.caller }

// push queues one frame without ever blocking. A full queue drops the frame and
// flags the connection: Next then emits a `resync` so the client refetches.
func (c *Conn) push(b []byte) {
	select {
	case <-c.done:
		return
	default:
	}
	select {
	case c.send <- b:
	default:
		c.h.dropped.Add(1)
		c.overflow.Store(true)
		select {
		case c.kick <- struct{}{}:
		default:
		}
	}
}

// Next blocks for the next outbound frame. A pending overflow is reported first, as
// a `resync` frame, ahead of whatever is still queued.
func (c *Conn) Next(ctx context.Context) ([]byte, error) {
	for {
		if c.overflow.CompareAndSwap(true, false) {
			c.h.resyncs.Add(1)
			return frame("resync", "", nil), nil
		}
		select {
		case b := <-c.send:
			return b, nil
		case <-c.kick:
		case <-c.done:
			return nil, ErrClosed
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Close unregisters the connection and unblocks Next. Idempotent.
func (c *Conn) Close() {
	c.once.Do(func() {
		close(c.done)
		c.h.remove(c)
	})
}

// Subscribe adds topics (invalid ones are returned as rejected). sinceSeq carries
// the per-`job:<id>` resume cursors. Snapshot topics are answered with their current
// snapshot, a cursor-carrying job topic with the events it missed.
func (c *Conn) Subscribe(topics []string, sinceSeq map[string]int64) (rejected []string) {
	c.h.mu.Lock()
	for _, t := range topics {
		if !ValidTopic(t) {
			rejected = append(rejected, t)
			continue
		}
		if _, ok := c.topics[t]; ok {
			continue
		}
		if len(c.topics) >= maxTopicsPerConn {
			rejected = append(rejected, t)
			continue
		}
		c.topics[t] = struct{}{}
		set := c.h.byTopic[t]
		if set == nil {
			set = map[*Conn]struct{}{}
			c.h.byTopic[t] = set
		}
		set[c] = struct{}{}
	}
	c.h.mu.Unlock()

	for _, t := range topics {
		if !ValidTopic(t) {
			continue
		}
		if c.h.snapFn(t) != nil {
			if b, ok := c.h.initialSnap(t); ok {
				c.push(b)
			}
			continue
		}
		if strings.HasPrefix(t, JobTopicPrefix) {
			if since, ok := sinceSeq[t]; ok {
				c.backfillJob(strings.TrimPrefix(t, JobTopicPrefix), since)
			}
		}
	}
	return rejected
}

func (c *Conn) backfillJob(id string, since int64) {
	fn := c.h.backfill.Load()
	if fn == nil {
		return
	}
	evs, err := (*fn)(id, since)
	if err != nil {
		return
	}
	if len(evs) > maxBackfillEvents {
		evs = evs[len(evs)-maxBackfillEvents:]
	}
	topic := JobTopic(id)
	for _, ev := range evs {
		c.push(frame("evt", topic, map[string]any{
			"kind": "event", "seq": ev.Seq, "type": ev.Type, "detail": ev.Detail, "at": ev.At,
		}))
	}
}

// Unsubscribe drops topics.
func (c *Conn) Unsubscribe(topics []string) {
	c.h.mu.Lock()
	defer c.h.mu.Unlock()
	for _, t := range topics {
		if _, ok := c.topics[t]; !ok {
			continue
		}
		delete(c.topics, t)
		c.h.dropSubLocked(t, c)
	}
}

// ErrorFrame tells a client one of its frames was refused.
func ErrorFrame(msg string, topics []string) []byte {
	b, err := json.Marshal(map[string]any{"t": "error", "msg": msg, "topics": topics})
	if err != nil {
		return []byte(`{"t":"error"}`)
	}
	return b
}

// PongFrame is the reply to a client `ping`.
func PongFrame() []byte { return frame("pong", "", nil) }

// Push queues an arbitrary pre-built frame (hello/pong style) for this connection.
func (c *Conn) Push(b []byte) { c.push(b) }

// frame marshals one wire frame: {"t":kind,"topic":...,"data":...}.
func frame(kind, topic string, data any) []byte {
	m := map[string]any{"t": kind}
	if topic != "" {
		m["topic"] = topic
	}
	if data != nil {
		if kind == "hello" {
			for k, v := range data.(map[string]any) {
				m[k] = v
			}
		} else {
			m["data"] = data
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte(`{"t":"resync"}`)
	}
	return b
}
