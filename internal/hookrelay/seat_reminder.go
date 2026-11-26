package hookrelay

import (
	"os"
	"strings"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/seat"
	"github.com/inhere/gofer/internal/stagenote"
)

// seatReminder appends the resident-seat block to the SessionStart context (design
// 2026-11-18 resident-sessions §4): which seat this session sits in, its purpose, its charter —
// and, on the first start of a session that has just taken the seat, the server's one-time
// continuation text. It runs on startup, resume and compact alike (every SessionStart), and
// adds nothing at all for a session that sits in no seat.
//
// A charter written as "@<path>" is read here, from the project root the server named, so the
// file's current content wins over anything cached at bind time.
func (r *runner) seatReminder(res Result, a client.AgentSession) Result {
	if a.Seat == nil || !CatchUpAgent(r.p.dialect()) {
		return res
	}
	in := seat.SectionInput{Name: a.Seat.Name, Purpose: a.Seat.Purpose, Continuation: a.Seat.Continuation, Lang: r.lang(),
		StageLine: stagenote.SessionStartLine(r.lang(), a.Stage)}
	switch {
	case a.Seat.CharterPath != "":
		in.Charter = r.readSeatCharter(*a.Seat)
	case a.Seat.Charter != "":
		in.Charter = a.Seat.Charter
	}
	text := seat.Section(in)
	if text == "" {
		return res
	}
	if res.Context != "" {
		res.Context += "\n\n"
	}
	res.Context += text
	return res
}

// readSeatCharter reads a seat's charter file (repository-relative to the project root the
// server named). A missing / unreadable file only costs the charter, never the seat block, and
// is logged.
func (r *runner) readSeatCharter(s client.SessionSeat) string {
	path := seat.JoinRootPath(s.Root, s.CharterPath)
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		r.log("seat charter %q unreadable: %v", path, err)
		return ""
	}
	return seat.Cut(strings.TrimSpace(string(b)), seat.CharterBudget)
}
