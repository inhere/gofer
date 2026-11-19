package commands

import (
	"strings"
	"testing"
)

// TestSeatListShowsJobIDsInFull: a terminal session id is shortened to its readable prefix, but the
// job id of an acp seat is the handle people copy, so it is printed whole.
func TestSeatListShowsJobIDsInFull(t *testing.T) {
	const jobID = "job-20261119-120000-ab12cd34"
	const sessionID = "3f9c1d2e-aaaa-bbbb-cccc-1234567890ab"
	stub := &seatStub{seats: []map[string]any{
		{"id": "seat-1", "name": "weekly", "project_key": "p1", "kind": "acp", "state": "running", "current_ref": jobID},
		{"id": "seat-2", "name": "desk", "project_key": "p1", "kind": "terminal", "state": "running", "current_ref": sessionID},
	}}
	seatTestStub(t, stub)
	prevProject := seatListOpts.project
	t.Cleanup(func() { seatListOpts.project = prevProject })
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "ls", "-p", "p1"})
	})
	if code != 0 {
		t.Fatalf("ls exit=%d out=%s", code, out)
	}
	if !strings.Contains(out, jobID) {
		t.Fatalf("the job id of an acp seat must be shown in full: %s", out)
	}
	if strings.Contains(out, sessionID) || !strings.Contains(out, sessionID[:8]) {
		t.Fatalf("a terminal session id is shown by its prefix only: %s", out)
	}
}

// TestSeatShowLabelsTheCharterAuthor: the author of the charter is not the charter — the label says
// who wrote it, and the text itself is printed further down.
func TestSeatShowLabelsTheCharterAuthor(t *testing.T) {
	stub := &seatStub{seats: []map[string]any{
		{"id": "seat-1", "name": "desk", "project_key": "p1", "kind": "terminal", "state": "none",
			"charter": "be brief", "charter_by": "session:sess-9"},
	}}
	seatTestStub(t, stub)
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "show", "desk@p1"})
	})
	if code != 0 {
		t.Fatalf("show exit=%d out=%s", code, out)
	}
	if !strings.Contains(out, "charter by: session:sess-9") {
		t.Fatalf("the charter's author must be labelled apart from the charter: %s", out)
	}
	if strings.Contains(out, "charter:  session:") {
		t.Fatalf("the old label reads as a charter named like the session: %s", out)
	}
}

// TestSeatBindShowsTheACPSessionJobInFull: the bind confirmation names the new job of an acp seat
// in full.
func TestSeatBindShowsTheACPSessionJobInFull(t *testing.T) {
	const jobID = "job-20261119-120000-ab12cd34"
	stub := &seatStub{
		seats:    []map[string]any{{"id": "seat-1", "name": "weekly", "project_key": "p1", "kind": "acp", "state": "none", "charter_by": "user"}},
		sessions: []map[string]any{{"session_id": "sid-current", "agent": "claude", "state": "idle"}},
	}
	seatTestStub(t, stub)
	prev := seatBindOpts.ref
	t.Cleanup(func() { seatBindOpts.ref = prev })
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "bind", "weekly@p1", "--ref", jobID})
	})
	if code != 0 {
		t.Fatalf("bind exit=%d out=%s", code, out)
	}
	if !strings.Contains(out, "now holds session "+jobID) {
		t.Fatalf("the bind confirmation must name the job in full: %s", out)
	}
}
