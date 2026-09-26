package job

import (
	"io"
	"log/slog"
	"sync"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/ptyrelay"
)

// Window sizes for a LIVE (streaming) session-id observation. Two windows, like the
// pty capture: claude prints its id at the start (head), codex prints the batch header
// first but a TUI-style banner can arrive anytime, and AGT-04's generic fallback reads
// the TAIL only. Each window is bounded, and each Write scans only the bytes that are
// new since the previous scan (plus streamOverlap so a match straddling a chunk
// boundary is still found) — a chatty agent must not pay a full rescan per write.
const (
	streamHeadBytes = 64 << 10
	streamTailBytes = 64 << 10
	streamOverlap   = 1 << 10
)

// streamSessionCapture observes a local cli-agent's text stdout/stderr and lands the
// session id on the job row the moment it appears (F-e). It is the text counterpart of
// the ndjson capture (which reads the agent's own structured stream) and of the pty
// capture (which reads the de-ANSI'd relay transcript): before it existed, a text
// agent's id was only picked up by the TERMINAL scan of the log files, so a serve
// restart in between left the row unresumable.
//
// One instance is shared by BOTH streams of a job, so a hit on stderr stops the scan on
// stdout (first writer wins, like every other capture path).
type streamSessionCapture struct {
	mu     sync.Mutex
	jobID  string
	agent  string
	reSrc  string
	onHit  func(sid string)
	strip  ptyrelay.Stripper
	head   []byte
	tail   []byte
	headAt int // bytes of head already scanned
	tailAt int // bytes of tail already scanned
	// scanned counts the bytes handed to the regex — the accounting the tests use to
	// prove the observation stays linear instead of rescanning history.
	scanned int64
	hit     bool
}

// newStreamSessionCapture builds an observer for one job's stream. onHit is called at
// most once, with the mutex held, from the goroutine that wrote the matching bytes.
func newStreamSessionCapture(jobID, agentKey, reSrc string, onHit func(sid string)) *streamSessionCapture {
	return &streamSessionCapture{jobID: jobID, agent: agentKey, reSrc: reSrc, onHit: onHit}
}

// streamSessionCaptureWriter is the io.WriteCloser handed to the runner: it feeds every
// byte to the observer BEFORE writing it through, so the id is persisted as early as
// the process emitted it.
type streamSessionCaptureWriter struct {
	w   io.WriteCloser
	cap *streamSessionCapture
}

func (w streamSessionCaptureWriter) Write(p []byte) (int, error) {
	w.cap.observe(p)
	return w.w.Write(p)
}

// Close delegates: the capture is a pure observer with nothing of its own to flush
// (the ANSI stripper's only held byte is a CR, which cannot hide an id).
func (w streamSessionCaptureWriter) Close() error { return w.w.Close() }

// observe strips ANSI, extends the windows and looks for the id. A fallback-regex
// agent is looked for in the TAIL window only (AGT-04: its banner is an exit banner,
// and the head would let an echoed `--resume <id>` win over the real one); every other
// agent gets head-then-tail, the cheap order.
func (c *streamSessionCapture) observe(p []byte) {
	if len(p) == 0 {
		return
	}
	text := c.strip.Write(p)
	if len(text) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hit {
		return
	}
	if !agent.IsFallbackCapture(c.reSrc) && len(c.head) < streamHeadBytes {
		if sid := c.scan(&c.head, &c.headAt, text, streamHeadBytes); sid != "" {
			c.record(sid)
			return
		}
	}
	if sid := c.scan(&c.tail, &c.tailAt, text, streamTailBytes); sid != "" {
		c.record(sid)
	}
}

// record marks the capture done and hands the id to the writer. The hit flag is the
// dedupe; the underlying persist is independently first-wins.
func (c *streamSessionCapture) record(sid string) {
	c.hit = true
	c.onHit(sid)
}

// scan extends one rolling window with text and runs the agent's regex over the
// newly-reachable region only. `at` tracks how much of the window the previous scan
// already covered; the region starts streamOverlap bytes earlier so a match split
// across two writes is still seen.
func (c *streamSessionCapture) scan(buf *[]byte, at *int, text []byte, max int) string {
	from := appendWindow(buf, *at, text, max)
	seg := (*buf)[from:]
	c.scanned += int64(len(seg))
	*at = len(*buf)
	if len(seg) == 0 {
		return ""
	}
	return CaptureSessionIDBytes(seg, c.reSrc)
}

// appendWindow appends text to the rolling window, keeping at most max trailing bytes,
// and returns the index scanning must begin at (the previously scanned end minus
// streamOverlap). Bytes dropped off the FRONT shift that index accordingly, so a full
// window never re-reads its own history.
func appendWindow(buf *[]byte, at int, text []byte, max int) int {
	b := *buf
	old, total := len(b), len(b)+len(text)
	switch {
	case total <= max:
		b = append(b, text...)
	case len(text) >= max:
		// The write alone fills the window: only its trailing max bytes survive.
		b = append(b[:0], text[len(text)-max:]...)
	default:
		keep := max - len(text)
		b = append(b[:0], b[old-keep:]...)
		b = append(b, text...)
	}
	*buf = b
	// `at` counts bytes of the OLD window; everything before (total-len(b)) was
	// dropped, so the old scan end now sits at at-(total-len(b)).
	from := at - (total - len(b)) - streamOverlap
	if from < 0 {
		from = 0
	}
	if from > len(b) {
		from = len(b)
	}
	return from
}

// captureStreamSession wraps a locally-run job's two writers in the F-e observer when
// there is something for it to find. It returns them unchanged for a remote runner (the
// executing machine mirrors already-projected streams here), an ndjson agent (the
// structured capture already persists its id live), an agent with no session_capture
// regex (exec, acp) or a job whose session id is already known (injected / resumed) or
// is interactive (that output flows through the pty relay, where the pty capture owns
// it).
func (s *Service) captureStreamSession(entry *jobEntry, jobID, runnerName string, stdout, stderr io.WriteCloser) (io.WriteCloser, io.WriteCloser) {
	if runnerName != builtinLocalRunner {
		return stdout, stderr
	}
	agentKey, _ := entryAgentAndDir(entry)
	ac, ok := s.agents.Get(agentKey)
	if !ok || ac.SessionCapture == "" || ac.NDJSONOutput() {
		return stdout, stderr
	}
	entry.mu.Lock()
	skip := entry.result.SessionID != "" || entry.result.Interactive
	entry.mu.Unlock()
	if skip {
		return stdout, stderr
	}
	reSrc := ac.SessionCapture
	cap := newStreamSessionCapture(jobID, agentKey, reSrc, func(sid string) {
		s.persistStreamSession(entry, jobID, agentKey, SessionCaptureBy(reSrc), sid)
	})
	return streamSessionCaptureWriter{w: stdout, cap: cap}, streamSessionCaptureWriter{w: stderr, cap: cap}
}

// persistStreamSession lands an id a text stream carried on the job row the moment it
// is seen (F-e) — the same first-wins narrow UPDATE the ndjson path uses, so it can
// neither resurrect a terminal row nor race finish()'s whole-row write. `source` is
// "stream", which distinguishes it from the ndjson ("ndjson") and pty ("pty") live
// captures in the job.session_captured audit row. Best-effort: a failed write only
// warns — the id is still in memory, so the terminal scan loses nothing.
func (s *Service) persistStreamSession(entry *jobEntry, jobID, agentKey, by, sessionID string) {
	if sessionID == "" {
		return
	}
	entry.mu.Lock()
	if entry.result.SessionID != "" {
		entry.mu.Unlock()
		return
	}
	entry.result.SessionID = sessionID
	entry.mu.Unlock()

	written, err := s.meta.SetJobSessionID(jobID, sessionID)
	if err != nil {
		slog.Warn("job.session_stream_persist", "job_id", jobID, "agent", agentKey, "err", err)
		return
	}
	if !written {
		return // another writer got there first; a second event would be a duplicate.
	}
	s.recordEvent(jobID, EventJobSessionCaptured, map[string]any{
		"agent": agentKey, "by": by, "source": "stream",
	})
}
