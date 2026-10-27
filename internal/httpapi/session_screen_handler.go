package httpapi

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/ptyrelay"
	"github.com/inhere/gofer/internal/store"
)

// Screen window bounds: the default is what a person reads in a terminal pane and
// the cap keeps one read small enough to answer an agent's tool call with.
const (
	defaultScreenLines = 50
	maxScreenLines     = 500
)

// sessionScreenResult is 「what does this session look like right now」: a
// read-only text window plus where it came from.
type sessionScreenResult struct {
	SessionID string `json:"session_id"`
	Agent     string `json:"agent,omitempty"`
	State     string `json:"state,omitempty"`
	// Source is the text's origin: "pty" — the pty transcript of the interactive
	// job that took the session over (path B's takeover, read the way `job logs`
	// reads it); "turns" — the session's recent relay turns, the same rows
	// `session show` prints; "message" — no turns at all, so its last message /
	// progress line stands in.
	Source string `json:"source"`
	// JobID is the pty job behind a "pty" screen (absent otherwise).
	JobID string `json:"job_id,omitempty"`
	// Text is plain text: every ANSI escape sequence removed.
	Text string `json:"text"`
}

// handleSessionScreen serves GET /v1/sessions/{sid}/screen?n=50 — the tail of what
// a session shows, for an agent orchestrating other sessions (it cannot look at
// anyone's terminal). Read-only, same permission as `session show`
// (session.read.others for somebody else's session).
func (s *Server) handleSessionScreen(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	if !s.mayReadSession(c, s.sessionOwnerOrUnknown(c)) {
		s.denySessionAction(c, authz.PermSessionReadOthers)
		return
	}
	n := defaultScreenLines
	if v, err := strconv.Atoi(c.Query("n")); err == nil && v > 0 {
		n = min(v, maxScreenLines)
	}
	res, err := s.sessionScreen(c.Param("sid"), n)
	if err != nil {
		writeError(c, relayStatus(err), "get session screen failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// sessionScreen builds the screen of one session: the takeover pty transcript when
// the session is carried by an interactive job, else its relay turns.
func (s *Server) sessionScreen(sid string, lines int) (sessionScreenResult, error) {
	// The turns are read even for the pty case: they are what makes the answer
	// useful when the job's transcript has nothing yet, and the read is the same
	// one `session show` does.
	d, err := s.relay.Get(sid, lines, "")
	if err != nil {
		return sessionScreenResult{}, err
	}
	a := d.Session
	res := sessionScreenResult{SessionID: a.SessionID, Agent: a.Agent, State: a.State, Source: "turns"}
	if a.HandedOffJobID != "" {
		if text, ok := s.ptyScreen(a.HandedOffJobID, lines); ok {
			res.Source, res.JobID, res.Text = "pty", a.HandedOffJobID, text
			return res, nil
		}
	}
	res.Text = formatScreenTurns(d.Turns, a.LastMessage, lines)
	if len(d.Turns) == 0 {
		res.Source = "message"
	}
	// Whatever the source, the caller gets readable text: a turn's message can carry
	// the escape sequences an agent pasted or its terminal printed.
	res.Text = ptyrelay.StripANSI(res.Text)
	return res, nil
}

// ptyScreen reads the tail of a pty job's transcript — the read `job logs` does
// for an interactive job (pty output never reaches stdout.log). ok is false when
// there is nothing to read that way (unknown job, not interactive, no transcript
// yet), so the caller falls back to the relay turns instead of answering an empty
// screen.
func (s *Server) ptyScreen(jobID string, lines int) (string, bool) {
	if s.jobs == nil {
		return "", false
	}
	res, ok := s.jobs.Get(jobID)
	if !ok || !res.Interactive || res.ResultDir == "" {
		return "", false
	}
	fs := store.NewFileStore(filepath.Dir(res.ResultDir))
	data, total, err := fs.ReadLogLines(jobID, store.StreamPty, lines, 0)
	if err != nil || total == 0 {
		return "", false
	}
	text := strings.TrimRight(ptyrelay.StripANSI(string(data)), "\n")
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	return text, true
}

// formatScreenTurns renders the last relay turns the way a reader walks a
// conversation — oldest at the top, newest at the bottom — followed by the
// session's last message when it has one. The rendered text is cut to its last
// `lines` lines, so `-n` means the same thing for a turn screen as for a pty one.
func formatScreenTurns(turns []*jobstore.PlanDecision, lastMessage string, lines int) string {
	var b strings.Builder
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		header := oneLineText(t.Title, 120)
		if header == "" {
			header = oneLineText(t.Question, 120)
		}
		fmt.Fprintf(&b, "[%s] %s\n", strings.ToUpper(t.State), header)
		if q := strings.TrimSpace(t.Question); q != "" {
			b.WriteString("agent> " + clipScreenText(q, 4000) + "\n")
		}
		human := strings.TrimSpace(t.Answer)
		if human == "" {
			human = "(waiting for a reply)"
		}
		b.WriteString("human> " + clipScreenText(human, 4000) + "\n")
	}
	if m := strings.TrimSpace(lastMessage); m != "" {
		b.WriteString("last message:\n" + clipScreenText(m, 4000) + "\n")
	}
	out := strings.TrimRight(b.String(), "\n")
	if lines <= 0 {
		return out
	}
	rows := strings.Split(out, "\n")
	if len(rows) > lines {
		rows = rows[len(rows)-lines:]
	}
	return strings.Join(rows, "\n")
}

// oneLineText collapses whitespace and bounds a single line of a turn header.
func oneLineText(s string, n int) string {
	return clipScreenText(strings.Join(strings.Fields(s), " "), n)
}

// clipScreenText bounds text to n runes with an ellipsis, cut on a rune boundary.
func clipScreenText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
