// Package pushhub is the in-process fan-out behind the browser's single /v1/ws
// connection (Q2). Publishers (job persist, the event tap, decision/session writes,
// worker presence) only ever call the non-blocking Notify* methods; the hub turns
// those into frames on per-connection bounded queues. A queue that fills drops the
// increment and flags the connection for a `resync`, so a slow browser can never stall
// recordEvent or persist.
//
// The package is a leaf: it knows topics, frames and queues, nothing about jobs,
// stores or HTTP (G022).
//
// A connection may carry a project scope (caller-scope ceilings, gofer-7798): it then
// only hears the changes of the projects its scope admits, and its snapshot topics are
// computed for that scope. The zero scope is unrestricted and keeps the original single
// shared frame per topic.
package pushhub

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
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
	TopicWork      = "work"
	// TopicWorkerEnrollments is a pure invalidation (no payload) for worker enrollment
	// requests; subscribing needs worker.manage (httpapi.wsTopicAllowed).
	TopicWorkerEnrollments = "worker-enrollments"

	JobTopicPrefix = "job:"
)

var globalTopics = map[string]bool{
	TopicStats: true, TopicPending: true, TopicJobs: true, TopicSessions: true,
	TopicRunners: true, TopicMeta: true, TopicPlans: true, TopicWorkflows: true, TopicSchedules: true, TopicWork: true,
	TopicWorkerEnrollments: true,
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
	// maxInvalProjects caps the project list one coalesced burst keeps; past it the burst
	// is treated as unscoped (a restricted connection hears it, which over-invalidates).
	maxInvalProjects = 50
)

// ErrTooManyConns is Register's refusal once a caller already holds the maximum.
var ErrTooManyConns = errors.New("pushhub: too many connections for this caller")

// ErrClosed is returned by Conn.Next once the connection or hub is closed.
var ErrClosed = errors.New("pushhub: connection closed")

// SnapFunc computes one snapshot topic's payload. It runs on the hub's own
// goroutines (never on a publisher's), and may be slow.
type SnapFunc func() (any, error)

// ScopedSnapFunc computes one snapshot topic's payload for a project restriction
// (caller-scope ceilings). scope is nil for the unrestricted computation — the frame
// every unrestricted connection shares; non-nil means the payload may only carry the
// rows of projects scope admits. Like SnapFunc it runs on the hub's goroutines.
type ScopedSnapFunc func(scope func(project string) bool) (any, error)

// ConnScope is the project restriction of one push connection. The zero value is an
// unrestricted connection: it receives every frame exactly as it did before the
// ceilings existed (that path is kept byte-identical on purpose).
type ConnScope struct {
	// Key fingerprints the restriction: connections with the same Key share one computed
	// snapshot frame and one cache entry. It must identify the project set, never the
	// connection. Ignored when Scope is nil; RegisterScoped makes it unique when a scoped
	// caller passes none, so two restrictions can never be confused for one another.
	Key string
	// Scope reports whether a project key is inside the restriction. It runs on the hub's
	// and the publishers' goroutines, so it must be cheap and must not panic.
	Scope func(project string) bool
}

// allows reports whether a change to project may be delivered under this scope. A
// restriction never hides a change whose project nobody named: the client then refetches,
// and what it refetches over REST is filtered server-side. Callers therefore decide only
// about changes that DO name a project.
func (s ConnScope) allows(project string) bool {
	return s.Scope == nil || project == "" || s.Scope(project)
}

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

	snapMu     sync.Mutex
	snapFns    map[string]SnapFunc
	snapScoped map[string]ScopedSnapFunc
	snapLast   map[string][]byte

	backfill atomic.Pointer[BackfillFunc]
	// jobProject resolves a job's project for a restricted `job:<id>` subscription
	// (SetJobProject).
	jobProject atomic.Pointer[func(jobID string) (project string, ok bool)]
	// scopeSeq numbers the restrictions of scoped connections that carry no Key.
	scopeSeq atomic.Uint64

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
		opts:       o,
		conns:      map[*Conn]struct{}{},
		byTopic:    map[string]map[*Conn]struct{}{},
		perCaller:  map[string]int{},
		snapFns:    map[string]SnapFunc{},
		snapScoped: map[string]ScopedSnapFunc{},
		snapLast:   map[string][]byte{},
		co:         map[string]*coalescer{},
	}
	intervals := map[string]time.Duration{
		TopicStats:             o.StatsInterval,
		TopicSessions:          o.SessionsInterval,
		TopicPending:           o.PendingInterval,
		TopicJobs:              o.InvalInterval,
		TopicRunners:           o.InvalInterval,
		TopicMeta:              o.InvalInterval,
		TopicPlans:             o.InvalInterval,
		TopicWorkflows:         o.InvalInterval,
		TopicSchedules:         o.InvalInterval,
		TopicWork:              o.InvalInterval,
		TopicWorkerEnrollments: o.InvalInterval,
	}
	for topic, iv := range intervals {
		h.co[topic] = &coalescer{h: h, topic: topic, interval: iv}
	}
	return h
}

// SetSnapshot installs the payload source of a snapshot topic (stats, pending) for an
// unrestricted connection.
func (h *Hub) SetSnapshot(topic string, fn SnapFunc) {
	h.snapMu.Lock()
	h.snapFns[topic] = fn
	h.snapMu.Unlock()
}

// SetScopedSnapshot installs the payload source of a snapshot topic for a restricted
// connection. A restricted connection whose topic has no scoped source gets NO snapshot
// at all — it never falls back to the unrestricted payload, which would carry the rows
// of every project (fail closed). The producers of the payload (the httpapi/ws_handler
// snapshot builders) own the actual per-project filtering.
func (h *Hub) SetScopedSnapshot(topic string, fn ScopedSnapFunc) {
	h.snapMu.Lock()
	h.snapScoped[topic] = fn
	h.snapMu.Unlock()
}

// SetJobProject installs the resolver a restricted connection uses to bind the project of
// a `job:<id>` topic it subscribes to (nil clears). It is called at subscribe time, never
// on the publish hot path. Without it a job topic stays unbound and its increments reach
// every subscriber — acceptable only because subscription itself is already gated by the
// caller's scope in httpapi (the ws topic check).
func (h *Hub) SetJobProject(fn func(jobID string) (project string, ok bool)) {
	if fn == nil {
		h.jobProject.Store(nil)
		return
	}
	h.jobProject.Store(&fn)
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
	return h.RegisterScoped(caller, ConnScope{})
}

// RegisterScoped admits a new connection for caller under a project restriction. A zero
// ConnScope registers an unrestricted connection (the same thing Register does).
func (h *Hub) RegisterScoped(caller string, scope ConnScope) (*Conn, error) {
	if scope.Scope == nil {
		scope = ConnScope{}
	} else if scope.Key == "" {
		// No fingerprint: give the restriction its own, so two scoped connections are
		// never handed one another's cached snapshot frame.
		scope.Key = "conn:" + strconv.FormatUint(h.scopeSeq.Add(1), 10)
	}
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
		scope:  scope,
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
	c.topicProject = nil
}

func (h *Hub) dropSubLocked(topic string, c *Conn) {
	set := h.byTopic[topic]
	delete(set, c)
	if len(set) == 0 {
		delete(h.byTopic, topic)
		h.snapMu.Lock()
		for k := range h.snapLast {
			if snapKeyOf(k) == topic { // nobody is keeping it fresh any more
				delete(h.snapLast, k)
			}
		}
		h.snapMu.Unlock()
		return
	}
	// Scoped connections each have their own cache entry: drop the ones whose scope no
	// subscriber holds any more (a stale scoped frame must never be handed to a later
	// connection of the same restriction).
	h.snapMu.Lock()
	live := make(map[string]bool, len(set))
	for o := range set {
		live[snapKey(topic, o.scope)] = true
	}
	for k := range h.snapLast {
		if snapKeyOf(k) == topic && !live[k] {
			delete(h.snapLast, k)
		}
	}
	h.snapMu.Unlock()
}

// snapKey is the snapshot cache key of one topic for one scope: the bare topic for an
// unrestricted connection (the historical single frame), topic + fingerprint otherwise.
func snapKey(topic string, sc ConnScope) string {
	if sc.Key == "" {
		return topic
	}
	return topic + scopeKeySep + sc.Key
}

const scopeKeySep = "\x1f"

// snapKeyOf is the topic a cache key belongs to.
func snapKeyOf(key string) string {
	if i := strings.Index(key, scopeKeySep); i >= 0 {
		return key[:i]
	}
	return key
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

// publishJob queues one pre-built frame to the subscribers of a `job:<id>` topic. A
// restricted connection only receives the increments of a job its scope admits: project
// is the project of the change that is being published when the publisher knows it, and
// otherwise the one remembered from the subscription (Conn.topicProject).
func (h *Hub) publishJob(topic, project string, b []byte) {
	for _, c := range h.subscribersOf(topic) {
		// An unrestricted subscriber takes the frame unconditionally: its path must stay
		// exactly the one it had before connections could be scoped (no extra lookup).
		if c.scope.Scope != nil && !c.scope.allows(c.jobProjectOr(topic, project)) {
			continue
		}
		c.push(b)
	}
}

// ---------------------------------------------------------------- publishers

// Notify marks a global topic changed. Snapshot topics (stats, pending) are
// recomputed, the rest get an `inval`; either way bursts are coalesced and the work
// happens on a hub goroutine, so Notify is safe on any hot path.
func (h *Hub) Notify(topic string) {
	if co := h.co[topic]; co != nil {
		co.poke("", "", "")
	}
}

// NotifyProject is Notify for a change whose project the publisher knows: a restricted
// connection hears it only if the project is inside its scope (an empty project is a
// bulk / id-less write and reaches every connection).
func (h *Hub) NotifyProject(topic, project string) {
	if co := h.co[topic]; co != nil {
		co.poke("", "", project)
	}
}

// NotifyJob reports that job id changed to status: it feeds the `jobs` list
// invalidation (with the id), the `job:<id>` topic, and stats.
func (h *Hub) NotifyJob(id, status string) { h.NotifyJobProject(id, status, "") }

// NotifyJobProject is NotifyJob for a change whose project the publisher knows (the
// jobstore.Change.Project of a single-row write). The project both narrows the `jobs`
// invalidation for restricted connections and filters the `job:<id>` increment.
func (h *Hub) NotifyJobProject(id, status, project string) {
	if co := h.co[TopicJobs]; co != nil {
		co.poke(id, status, project)
	}
	topic := JobTopic(id)
	if h.Subscribers(topic) > 0 {
		h.publishJob(topic, project, frame("evt", topic, map[string]any{"kind": "status", "id": id, "status": status}))
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
	h.publishJob(topic, "", frame("evt", topic, map[string]any{
		"kind": "event", "seq": ev.Seq, "type": ev.Type, "detail": ev.Detail, "at": ev.At,
	}))
}

// PublishJobKind sends a typed hint (e.g. "interaction") to a job's subscribers.
func (h *Hub) PublishJobKind(id, kind string) {
	topic := JobTopic(id)
	if h.Subscribers(topic) == 0 {
		return
	}
	h.publishJob(topic, "", frame("evt", topic, map[string]any{"kind": kind, "id": id}))
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
	case t == "job.awaiting_approval", strings.HasPrefix(t, "job.hold_"):
		// A job parking for approval, and the approval / rejection / expiry that ends
		// the wait, all change what the bell lists (gofer-5wvc): the pending snapshot
		// carries the held jobs, so a browser hears about a person's decision and about
		// a job that parked. The literals are internal/job's EventJobAwaitingApproval /
		// EventJobHold* (this package is a leaf and must not import job).
		h.Notify(TopicPending)
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

// snapFn returns the payload source of one topic for one scope: the unrestricted source
// for an unrestricted connection, the scoped one otherwise. A restricted connection whose
// topic has no scoped source gets no snapshot at all rather than the unrestricted payload
// (fail closed).
func (h *Hub) snapFn(topic string, sc ConnScope) (SnapFunc, bool) {
	h.snapMu.Lock()
	defer h.snapMu.Unlock()
	if sc.Scope != nil {
		fn := h.snapScoped[topic]
		if fn == nil {
			return nil, false
		}
		return func() (any, error) { return fn(sc.Scope) }, true
	}
	fn := h.snapFns[topic]
	if fn == nil {
		return nil, false
	}
	return fn, true
}

// isSnapshot reports whether a topic's subscribers get computed payloads rather than
// invalidations.
func (h *Hub) isSnapshot(topic string) bool {
	if _, ok := h.snapFn(topic, ConnScope{}); ok {
		return true
	}
	h.snapMu.Lock()
	defer h.snapMu.Unlock()
	return h.snapScoped[topic] != nil
}

// compute builds and caches the framed snapshot of topic for one scope.
func (h *Hub) compute(topic string, sc ConnScope) ([]byte, bool) {
	fn, ok := h.snapFn(topic, sc)
	if !ok {
		return nil, false
	}
	data, err := fn()
	if err != nil {
		return nil, false
	}
	b := frame("snap", topic, data)
	h.snapMu.Lock()
	h.snapLast[snapKey(topic, sc)] = b
	h.snapMu.Unlock()
	return b, true
}

// initialSnap is what a new subscriber gets: the cached frame when somebody of the same
// scope is already keeping it fresh, else a fresh computation.
func (h *Hub) initialSnap(topic string, sc ConnScope) ([]byte, bool) {
	h.snapMu.Lock()
	b, ok := h.snapLast[snapKey(topic, sc)]
	h.snapMu.Unlock()
	if ok {
		return b, true
	}
	return h.compute(topic, sc)
}

// ---------------------------------------------------------------- coalescer

// jobInval is one `jobs` invalidation entry: the job's new status and the project of the
// change (empty when the writer could not name it).
type jobInval struct {
	status  string
	project string
}

// emission is what one coalesced burst of a topic amounts to.
type emission struct {
	// projects names the projects the burst touched; unscoped is set when at least one
	// poke named none (a bulk / id-less write), which every connection treats as global.
	projects map[string]struct{}
	unscoped bool
	// ids / allIDs are the `jobs` payload (job id -> new status / project).
	ids    map[string]jobInval
	allIDs bool
}

// addProject records one project; overflow falls back to unscoped, which over-invalidates
// a restricted connection instead of dropping a change it should have heard about.
func (e *emission) addProject(project string) {
	if project == "" {
		e.unscoped = true
		return
	}
	if e.unscoped {
		return
	}
	if e.projects == nil {
		e.projects = map[string]struct{}{}
	}
	if len(e.projects) >= maxInvalProjects {
		e.unscoped = true
		return
	}
	e.projects[project] = struct{}{}
}

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
	em      emission
}

func (c *coalescer) poke(id, status, project string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id != "" {
		if c.em.ids == nil {
			c.em.ids = map[string]jobInval{}
		}
		if len(c.em.ids) >= maxJobsInvalIDs {
			c.em.allIDs = true
		} else {
			c.em.ids[id] = jobInval{status: status, project: project}
		}
	}
	c.em.addProject(project)
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
	em := c.em
	c.em = emission{}
	c.mu.Unlock()

	c.h.emit(c.topic, em)

	c.mu.Lock()
	c.running = false
	if c.dirty && c.timer == nil {
		c.scheduleLocked()
	}
	c.mu.Unlock()
}

// scopeGroup is the subscribers of one topic that share one project restriction (the
// unrestricted connections form the group whose scope is the zero value).
type scopeGroup struct {
	scope ConnScope
	conns []*Conn
}

// hears reports whether this group should be sent the burst. An unrestricted group always
// hears it; a restricted group hears it when the burst named no project (a bulk / id-less
// write, which every connection treats as global) or named one its scope admits.
func (g scopeGroup) hears(em emission) bool {
	if g.scope.Scope == nil || em.unscoped {
		return true
	}
	for p := range em.projects {
		if g.scope.Scope(p) {
			return true
		}
	}
	return false
}

// scopeGroups splits subscribers by project restriction, keeping the first-seen order.
func scopeGroups(subs []*Conn) []scopeGroup {
	out := make([]scopeGroup, 0, 2)
	idx := make(map[string]int, len(subs))
	for _, c := range subs {
		i, ok := idx[c.scope.Key]
		if !ok {
			i = len(out)
			idx[c.scope.Key] = i
			out = append(out, scopeGroup{scope: c.scope})
		}
		out[i].conns = append(out[i].conns, c)
	}
	return out
}

// emit sends one coalesced notification: a recomputed snapshot for snapshot topics,
// otherwise an inval. Without subscribers it does nothing (stats is not computed for
// an empty room). Unrestricted connections share one frame — exactly the frame they
// received before the caller-scope ceilings existed.
func (h *Hub) emit(topic string, em emission) {
	subs := h.subscribersOf(topic)
	if len(subs) == 0 {
		return
	}
	if h.isSnapshot(topic) {
		for _, g := range scopeGroups(subs) {
			if b, ok := h.compute(topic, g.scope); ok {
				for _, c := range g.conns {
					c.push(b)
				}
			}
		}
		return
	}
	for _, g := range scopeGroups(subs) {
		if !g.hears(em) {
			continue
		}
		b := invalFrame(topic, em, g.scope)
		for _, c := range g.conns {
			c.push(b)
		}
	}
}

// invalFrame builds one invalidation frame for one project restriction. Only `jobs`
// carries a payload: its id list is narrowed to the scope, and a job whose project the
// writer could not name is left out of a restricted frame (the frame still goes out, so
// the client refetches, and the list it refetches is filtered server-side). Every other
// topic is a payload-free "something changed".
func invalFrame(topic string, em emission, sc ConnScope) []byte {
	data := map[string]any{}
	if topic != TopicJobs {
		return frame("inval", topic, data)
	}
	if em.allIDs {
		// The id list overflowed: every connection refetches, a restricted one through its
		// own scoped REST endpoints.
		data["all"] = true
		return frame("inval", topic, data)
	}
	if len(em.ids) == 0 {
		return frame("inval", topic, data)
	}
	list := make([]map[string]string, 0, len(em.ids))
	for id, inv := range em.ids {
		if sc.Scope != nil && (inv.project == "" || !sc.Scope(inv.project)) {
			continue
		}
		list = append(list, map[string]string{"id": id, "status": inv.status})
	}
	if len(list) > 0 {
		data["jobs"] = list
	}
	return frame("inval", topic, data)
}

// ---------------------------------------------------------------- conn

// Conn is one browser connection: a bounded outbound queue plus its subscriptions.
type Conn struct {
	h      *Hub
	caller string
	// scope is the connection's project restriction (zero = unrestricted). It never
	// changes after RegisterScoped, so readers need no lock.
	scope ConnScope
	send  chan []byte
	kick  chan struct{}
	done  chan struct{}
	once  sync.Once

	overflow atomic.Bool
	topics   map[string]struct{} // guarded by h.mu
	// topicProject is the project of each `job:<id>` subscription, resolved at subscribe
	// time for a restricted connection (guarded by h.mu).
	topicProject map[string]string
}

// jobProjectOr is the project to judge topic's increments by: the one the publisher named
// for this change, else the one remembered from the subscription (empty when unknown).
func (c *Conn) jobProjectOr(topic, project string) string {
	if project != "" {
		return project
	}
	c.h.mu.RLock()
	defer c.h.mu.RUnlock()
	return c.topicProject[topic]
}

// Caller is the authenticated identity the connection was registered under.
func (c *Conn) Caller() string { return c.caller }

// Topics copies the topics this connection is currently subscribed to. The push caller uses it to
// re-compute what its subscriptions need: the permission rule of design §6 is declared per topic,
// and a permission can go away while the connection is open.
func (c *Conn) Topics() []string {
	c.h.mu.RLock()
	defer c.h.mu.RUnlock()
	out := make([]string, 0, len(c.topics))
	for t := range c.topics {
		out = append(out, t)
	}
	return out
}

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
	var boundJobs []string
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
		if c.scope.Scope != nil && strings.HasPrefix(t, JobTopicPrefix) {
			boundJobs = append(boundJobs, t)
		}
	}
	c.h.mu.Unlock()

	// A restricted connection binds the project of each job topic it subscribes to, so a
	// later increment can be filtered without a lookup on the publish hot path.
	c.bindJobProjects(boundJobs)

	for _, t := range topics {
		if !ValidTopic(t) {
			continue
		}
		if _, ok := c.h.snapFn(t, c.scope); ok {
			if b, ok := c.h.initialSnap(t, c.scope); ok {
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

// bindJobProjects resolves and remembers the project of job topics for this restricted
// connection. A job the resolver cannot name stays unbound: its increments then reach
// every subscriber, which is acceptable only because the subscription itself is gated by
// the caller's scope in httpapi (the ws topic check).
func (c *Conn) bindJobProjects(topics []string) {
	if len(topics) == 0 {
		return
	}
	fn := c.h.jobProject.Load()
	if fn == nil {
		return
	}
	proj := make(map[string]string, len(topics))
	for _, t := range topics {
		if p, ok := (*fn)(strings.TrimPrefix(t, JobTopicPrefix)); ok && p != "" {
			proj[t] = p
		}
	}
	if len(proj) == 0 {
		return
	}
	c.h.mu.Lock()
	if c.topicProject == nil {
		c.topicProject = map[string]string{}
	}
	for t, p := range proj {
		c.topicProject[t] = p
	}
	c.h.mu.Unlock()
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
		delete(c.topicProject, t)
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
