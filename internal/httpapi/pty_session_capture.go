package httpapi

import (
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/ptyrelay"
)

// ptyCaptureHeadBytes / ptyCaptureTailBytes are the two windows a TUI's session
// id is looked for in (PTY-01 §四): claude prints it near the start, codex prints
// it on the way out, and both are hidden behind ANSI escapes.
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
	return &ptySessionCapture{srv: s, jobID: res.ID, reSrc: ac.SessionCapture}
}

// observe is the relay's OutputObserver: de-ANSI the chunk, extend both windows,
// and look for the id in each (the head first — a hit there is the cheapest).
func (c *ptySessionCapture) observe(chunk []byte) {
	if c.hit || len(chunk) == 0 {
		return
	}
	text := c.strip.Write(chunk)
	if len(text) == 0 {
		return
	}
	c.head = appendWindow(c.head, text, ptyCaptureHeadBytes)
	c.tail = appendTail(c.tail, text, ptyCaptureTailBytes)
	if sid := job.CaptureSessionIDBytes(c.head, c.reSrc); sid != "" {
		c.record(sid)
		return
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

func (c *ptySessionCapture) record(sid string) {
	c.hit = true
	c.srv.jobs.SetSessionID(c.jobID, sid)
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
