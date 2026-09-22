package httpapi

import (
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/ptyrelay"
)

// ptyCaptureHeadBytes / ptyCaptureTailBytes are the two windows a TUI's session
// id is looked for in (PTY-01 §四): claude prints it near the start, codex prints
// it on the way out, and both are hidden behind ANSI escapes.
//
// AGT-04's generic fallback reads the TAIL only, and it needs no window of its own:
// its own bound (job's 4KB terminal-log window) is far smaller than this one, so the
// tail below is already the larger of the two.
const (
	ptyCaptureHeadBytes = 64 * 1024
	ptyCaptureTailBytes = 64 * 1024
)

// ptySessionCapture extracts the agent CLI's session id from a live pty stream.
//
// The previous observer kept only the FIRST 64KB of RAW output, which is why an
// interactive job never produced a session id (h-aii-…/PTY-01): the TUI's
// session-id banner comes at the END and is wrapped in ANSI sequences. This
// capture de-ANSI's every chunk (state carried across chunk boundaries), keeps a
// head window AND a rolling tail window, and is re-run over the tail once the
// relay closes — the exit banner is written in the last milliseconds before the
// child dies, after which no further chunk arrives.
type ptySessionCapture struct {
	srv   *Server
	jobID string
	agent string
	reSrc string

	strip ptyrelay.Stripper
	head  []byte
	tail  []byte
	hit   bool
}

// newPtySessionCapture builds the capture for a job, or nil when there is nothing
// to look for: no job service/agent registry, an already-known session id (the
// inject path), or an agent without a session_capture regex.
func (s *Server) newPtySessionCapture(res job.JobResult) *ptySessionCapture {
	if s == nil || s.jobs == nil || s.agents == nil || res.SessionID != "" {
		return nil
	}
	ac, ok := s.agents.Get(res.Agent)
	if !ok || ac.SessionCapture == "" {
		return nil
	}
	return &ptySessionCapture{srv: s, jobID: res.ID, agent: res.Agent, reSrc: ac.SessionCapture}
}

// observe is the relay's OutputObserver: de-ANSI the chunk, extend the windows, and
// look for the id in the window that suits the regex (the head first — a hit there
// is the cheapest).
//
// AGT-04: the GENERIC fallback regex reads the rolling TAIL only. Its banner is an
// exit banner, so the frozen head window — which exists for claude, whose id is
// printed at startup — has nothing to offer it, and reading the head first would let
// an early `--resume <id>` the TUI merely echoed win over the real one. A window
// nothing scans is not kept either.
func (c *ptySessionCapture) observe(chunk []byte) {
	if c.hit || len(chunk) == 0 {
		return
	}
	text := c.strip.Write(chunk)
	if len(text) == 0 {
		return
	}
	fallback := agent.IsFallbackCapture(c.reSrc)
	if !fallback {
		c.head = appendWindow(c.head, text, ptyCaptureHeadBytes)
	}
	c.tail = appendTail(c.tail, text, ptyCaptureTailBytes)
	if !fallback {
		if sid := job.CaptureSessionIDBytes(c.head, c.reSrc); sid != "" {
			c.record(sid)
			return
		}
	}
	c.scanTail()
}

// close is the relay's WithCloseHook: the recorder has stopped, so the tail now
// holds the exit banner. The trailing byte of a held CR is flushed first, so a
// banner printed as the very last output is still scanned.
func (c *ptySessionCapture) close() {
	if c.hit {
		return
	}
	if tail := c.strip.Flush(); len(tail) > 0 {
		c.tail = appendTail(c.tail, tail, ptyCaptureTailBytes)
	}
	c.scanTail()
}

func (c *ptySessionCapture) scanTail() {
	if c.hit {
		return
	}
	if sid := job.CaptureSessionIDBytes(c.tail, c.reSrc); sid != "" {
		c.record(sid)
	}
}

// record lands the id on the job and records the audit row. The event is what makes
// a capture visible at all (AGT-04): for an interactive TUI job the id usually
// arrives HERE, mid-stream, long before the terminal scan — and `by: "fallback"` is
// the signal that this agent has no session_capture of its own yet.
func (c *ptySessionCapture) record(sid string) {
	c.hit = true
	c.srv.jobs.SetSessionID(c.jobID, sid)
	c.srv.jobs.RecordJobEvent(c.jobID, job.EventJobSessionCaptured, map[string]any{
		"agent": c.agent, "by": job.SessionCaptureBy(c.reSrc), "source": "pty",
	})
}

// appendWindow appends text to buf but never lets buf exceed max: once it is
// full the window stops growing (the head is frozen at the first max bytes).
func appendWindow(buf, text []byte, max int) []byte {
	if len(buf) >= max {
		return buf
	}
	if remain := max - len(buf); len(text) > remain {
		text = text[:remain]
	}
	return append(buf, text...)
}

// appendTail appends text and retains only the trailing max bytes.
func appendTail(buf, text []byte, max int) []byte {
	buf = append(buf, text...)
	if len(buf) > max {
		buf = append(buf[:0:0], buf[len(buf)-max:]...)
	}
	return buf
}
