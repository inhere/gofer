package messenger

import (
	"strings"
	"sync"
	"time"
)

// Status values reported by Manager.Status / Snapshot.Status.
const (
	StatusStopped = "stopped" // no resident process
	StatusIdle    = "idle"    // process alive, no request in flight
	StatusBusy    = "busy"    // a request is queued or in flight
)

const (
	// MaxDeliveries is how many recent deliveries a runner remembers.
	MaxDeliveries = 20
	// MaxStderrTail caps the stderr ring buffer (bytes).
	MaxStderrTail = 4096
	// MaxRotations is how many recent resident-process rebuilds a runner remembers.
	MaxRotations = 10
	// maxSummaryRunes caps the message excerpt stored per delivery.
	maxSummaryRunes = 200
)

// Rebuild reasons recorded on every resident-process rebuild (Snapshot.Rotations).
const (
	ReasonIdle        = "idle"         // the idle timer stopped it
	ReasonFailed      = "failed"       // the process died or the request failed
	ReasonMaxMessages = "max_messages" // it reached the message-count rotation limit
	ReasonMaxAge      = "max_age"      // it reached the lifetime rotation limit
)

// Usage is the token / cost accounting the stream-json result frames report. A
// process whose result frames carry none keeps all zeros (never a guess).
type Usage struct {
	InputTokens         int64   `json:"input_tokens,omitempty"`
	OutputTokens        int64   `json:"output_tokens,omitempty"`
	CacheReadTokens     int64   `json:"cache_read_input_tokens,omitempty"`
	CacheCreationTokens int64   `json:"cache_creation_input_tokens,omitempty"`
	CostUSD             float64 `json:"cost_usd,omitempty"`
}

// add accumulates one result frame's accounting.
func (u *Usage) add(in, out, cacheRead, cacheCreation int64, cost float64) {
	u.InputTokens += in
	u.OutputTokens += out
	u.CacheReadTokens += cacheRead
	u.CacheCreationTokens += cacheCreation
	u.CostUSD += cost
}

// empty reports whether nothing was ever reported, so a snapshot omits it.
func (u Usage) empty() bool { return u == Usage{} }

// Rotation is one resident-process rebuild: why it happened and what the
// retired process had handled before it (gofer-li3t).
type Rotation struct {
	At       int64  `json:"at"`              // unix seconds of the rebuild
	Reason   string `json:"reason"`          // idle | failed | max_messages | max_age
	Messages int64  `json:"messages,omitempty"` // messages the retired process handled
	AgeSec   int64  `json:"age_sec,omitempty"`  // how long it lived
	Usage    *Usage `json:"usage,omitempty"`    // token / cost totals it reported
}

// Delivery is the summary of one request sent through the resident process.
// Message holds a short excerpt (never the full text) so the history is useful
// without becoming a second message log.
type Delivery struct {
	At         int64  `json:"at"`                // unix seconds the request started
	Op         string `json:"op,omitempty"`      // "send" | "list_agents"
	Target     string `json:"target,omitempty"`  // target session name (send)
	Message    string `json:"message,omitempty"` // excerpt, <= 200 runes
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// Snapshot is a point-in-time, JSON-friendly view of one runner's messenger.
// Times are unix seconds (0 = never). Deliveries are newest first.
type Snapshot struct {
	Status       string     `json:"status"`
	StartedAt    int64      `json:"started_at,omitempty"`
	LastUsedAt   int64      `json:"last_used_at,omitempty"`
	IdleDeadline int64      `json:"idle_deadline,omitempty"`
	// Messages and AgeSec describe the CURRENT resident process (0 when none is
	// running): how many messages it handled and how long it has lived. Usage is
	// what its result frames reported for tokens / cost. MaxMessages and MaxAgeSec
	// are the rotation limits in force (0 = unlimited), so a reader can tell how
	// close the process is to being rebuilt.
	Messages    int64      `json:"messages,omitempty"`
	AgeSec      int64      `json:"age_sec,omitempty"`
	Usage       *Usage     `json:"usage,omitempty"`
	MaxMessages int        `json:"max_messages,omitempty"`
	MaxAgeSec   int        `json:"max_age_sec,omitempty"`
	Rotations   []Rotation `json:"rotations,omitempty"` // recent rebuilds, newest first
	Deliveries  []Delivery `json:"deliveries,omitempty"`
	StderrTail  string     `json:"stderr_tail,omitempty"`
}

// runnerStats outlives the resident process: a crash or idle exit must not
// erase the history and stderr that explain it.
type runnerStats struct {
	busy       int
	startedAt  int64
	lastUsedAt int64
	deliveries []Delivery // oldest first, capped at MaxDeliveries
	rotations  []Rotation // oldest first, capped at MaxRotations
	stderr     *tailBuffer
}

func newRunnerStats() *runnerStats { return &runnerStats{stderr: &tailBuffer{max: MaxStderrTail}} }

func (s *runnerStats) record(d Delivery) {
	s.deliveries = append(s.deliveries, d)
	if over := len(s.deliveries) - MaxDeliveries; over > 0 {
		s.deliveries = append([]Delivery(nil), s.deliveries[over:]...)
	}
}

func (s *runnerStats) recordRotation(r Rotation) {
	s.rotations = append(s.rotations, r)
	if over := len(s.rotations) - MaxRotations; over > 0 {
		s.rotations = append([]Rotation(nil), s.rotations[over:]...)
	}
}

// tailBuffer keeps the last max bytes written to it. It implements io.Writer
// so io.Copy can feed it from the child's stderr.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append([]byte(nil), t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	// The cut may land inside a multi-byte rune; drop the broken prefix.
	return strings.ToValidUTF8(string(t.buf), "")
}

// Snapshot returns the runner's current view. It never blocks on an in-flight
// request: every field is read from state guarded by short-held locks.
func (m *Manager) Snapshot(runner string) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.stats[runner]
	out := Snapshot{Status: StatusStopped, MaxMessages: m.maxMessages, MaxAgeSec: int(m.maxAge / time.Second)}
	var alive bool
	if p := m.processes[runner]; p != nil && !p.gone() {
		alive = true
		out.IdleDeadline = p.deadline.Load()
		out.Messages = p.messages.Load()
		out.AgeSec = int64(p.age(time.Now()) / time.Second)
		if u := p.usageSnapshot(); !u.empty() {
			out.Usage = &u
		}
	}
	if st != nil {
		out.StartedAt, out.LastUsedAt = st.startedAt, st.lastUsedAt
		out.StderrTail = st.stderr.String()
		for i := len(st.deliveries) - 1; i >= 0; i-- {
			out.Deliveries = append(out.Deliveries, st.deliveries[i])
		}
		for i := len(st.rotations) - 1; i >= 0; i-- {
			out.Rotations = append(out.Rotations, st.rotations[i])
		}
		if st.busy > 0 {
			out.Status = StatusBusy
		}
	}
	if out.Status != StatusBusy && alive {
		out.Status = StatusIdle
	}
	if !alive {
		out.StartedAt = 0
		out.Messages, out.AgeSec, out.Usage = 0, 0, nil
		if out.Status != StatusBusy {
			out.IdleDeadline = 0
		}
	}
	return out
}

func (m *Manager) statsFor(runner string) *runnerStats {
	st := m.stats[runner]
	if st == nil {
		st = newRunnerStats()
		m.stats[runner] = st
	}
	return st
}

// begin marks a request as queued/in flight; the returned func records its
// outcome and clears the busy mark.
func (m *Manager) begin(runner, op, target, message string) func(err error) {
	start := time.Now()
	m.mu.Lock()
	m.statsFor(runner).busy++
	m.mu.Unlock()
	return func(err error) {
		d := Delivery{At: start.Unix(), Op: op, Target: target, Message: excerpt(message), OK: err == nil,
			DurationMS: time.Since(start).Milliseconds()}
		if err != nil {
			d.Error = excerpt(err.Error())
		}
		m.mu.Lock()
		st := m.statsFor(runner)
		if st.busy > 0 {
			st.busy--
		}
		st.lastUsedAt = time.Now().Unix()
		st.record(d)
		m.mu.Unlock()
	}
}

func excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if rs := []rune(s); len(rs) > maxSummaryRunes {
		return string(rs[:maxSummaryRunes]) + "…"
	}
	return s
}

// originalMessage extracts the forwarded text from the messenger prompt, which has
// the shape "<instruction>\n[来自 web，<operator>] <text>" for a person's message or
// "<instruction>\n[gofer 通知] <text>" for a gofer system notice. The brackets of
// the notice's own tags are skipped so the history shows the notice text.
func originalMessage(prompt string) string {
	if i := strings.LastIndex(prompt, "] "); i >= 0 && strings.Contains(prompt[:i], "[") {
		return strings.TrimSpace(prompt[i+2:])
	}
	return strings.TrimSpace(prompt)
}
