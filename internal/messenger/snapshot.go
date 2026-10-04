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
	// maxSummaryRunes caps the message excerpt stored per delivery.
	maxSummaryRunes = 200
)

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
	Deliveries   []Delivery `json:"deliveries,omitempty"`
	StderrTail   string     `json:"stderr_tail,omitempty"`
}

// runnerStats outlives the resident process: a crash or idle exit must not
// erase the history and stderr that explain it.
type runnerStats struct {
	busy       int
	startedAt  int64
	lastUsedAt int64
	deliveries []Delivery // oldest first, capped at MaxDeliveries
	stderr     *tailBuffer
}

func newRunnerStats() *runnerStats { return &runnerStats{stderr: &tailBuffer{max: MaxStderrTail}} }

func (s *runnerStats) record(d Delivery) {
	s.deliveries = append(s.deliveries, d)
	if over := len(s.deliveries) - MaxDeliveries; over > 0 {
		s.deliveries = append([]Delivery(nil), s.deliveries[over:]...)
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
	out := Snapshot{Status: StatusStopped}
	var alive bool
	if p := m.processes[runner]; p != nil && !p.gone() {
		alive = true
		out.IdleDeadline = p.deadline.Load()
	}
	if st != nil {
		out.StartedAt, out.LastUsedAt = st.startedAt, st.lastUsedAt
		out.StderrTail = st.stderr.String()
		for i := len(st.deliveries) - 1; i >= 0; i-- {
			out.Deliveries = append(out.Deliveries, st.deliveries[i])
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

// originalMessage extracts the user's text from the messenger prompt, which has
// the shape "<instruction>\n[来自 web，<operator>] <text>".
func originalMessage(prompt string) string {
	if i := strings.LastIndex(prompt, "] "); i >= 0 && strings.Contains(prompt[:i], "[") {
		return strings.TrimSpace(prompt[i+2:])
	}
	return strings.TrimSpace(prompt)
}
