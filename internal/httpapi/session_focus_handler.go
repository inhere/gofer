package httpapi

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/mail"
)

// The session focus (设计 2026-10-22 D2): what this session was doing before its
// context was compacted or the session was resumed. `gofer repo prime --session
// <sid> --source <compact|resume>` asks for it and appends it after the
// repository-level prime, so a compacted agent gets its own claim / plan / job /
// letter state back instead of only the repository's.
//
// One request returns all of it: the round trips prime can afford are few, and a
// section stitched from several calls would be read at several different times.
const (
	// sessionFocusPlanLimit caps the supervised plans listed (a session
	// supervises a handful, never a page).
	sessionFocusPlanLimit = 5
	// sessionFocusMailLimit caps the unreplied letters listed.
	sessionFocusMailLimit = 5
	// sessionFocusBodyRunes caps one letter's summary.
	sessionFocusBodyRunes = 120
)

// sessionFocusPlan is one open plan the session supervises.
type sessionFocusPlan struct {
	PlanID   string `json:"plan_id"`
	Title    string `json:"title,omitempty"`
	Status   string `json:"status"`
	Progress int    `json:"progress"`
	Total    int    `json:"total"`
	// Next is the first unfinished todo ("" = nothing queued), Blocked the item a
	// failed chain job parked the plan on ("" = not blocked).
	Next    string `json:"next,omitempty"`
	Blocked string `json:"blocked,omitempty"`
}

// sessionFocusMail is one letter addressed to the session that it has not
// answered yet.
type sessionFocusMail struct {
	ID        int64  `json:"id"`
	From      string `json:"from,omitempty"`
	Subject   string `json:"subject,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`
}

// sessionFocusView is GET /v1/sessions/{sid}/focus.
type sessionFocusView struct {
	SessionID  string `json:"session_id"`
	Agent      string `json:"agent,omitempty"`
	Title      string `json:"title,omitempty"`
	Topic      string `json:"topic,omitempty"`
	ProjectKey string `json:"project_key,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	// Caller is the authenticated caller that registered the session; prime
	// matches a claimed issue's assignee against it.
	Caller string `json:"caller,omitempty"`
	// StartedAt anchors the "claimed by this session" inference: an in_progress
	// issue touched after the session started is a probable claim of this session.
	StartedAt int64 `json:"started_at,omitempty"`

	Plans []sessionFocusPlan `json:"plans,omitempty"`
	Jobs  []sessionWatchView `json:"jobs,omitempty"`
	Mail  []sessionFocusMail `json:"mail,omitempty"`
}

// handleSessionFocus is GET /v1/sessions/{sid}/focus: the session's own
// supervision state, for the re-injection prime does after a compact / resume.
// It reads only (nothing is marked delivered), so a hook may call it freely.
func (s *Server) handleSessionFocus(c *rux.Context) {
	if !s.relayReady(c) {
		return
	}
	a, ok := s.sessionForAccess(c, "read session focus")
	if !ok {
		return
	}
	if !s.mayReadSession(c, a.CallerID) {
		s.denySessionAction(c, authz.PermSessionReadOthers)
		return
	}
	view := sessionFocusView{
		SessionID: a.SessionID, Agent: a.Agent, Title: a.Title, Topic: a.Topic,
		ProjectKey: a.ProjectKey, Cwd: a.Cwd, Caller: a.CallerID, StartedAt: a.StartedAt,
	}
	view.Plans = s.sessionFocusPlans(a.SessionID)
	if watches, err := s.relay.JobWatches(a.SessionID); err == nil {
		out := make([]sessionWatchView, 0, len(watches))
		for _, w := range watches {
			if wv, ok := s.sessionWatchView(w); ok {
				out = append(out, wv)
			}
		}
		view.Jobs = out
	}
	view.Mail = s.sessionFocusMail(a.SessionID)
	c.JSON(http.StatusOK, view)
}

// sessionFocusPlans lists the open (or blocked) plans this session supervises,
// newest first, with their progress and the item to pick up.
func (s *Server) sessionFocusPlans(sid string) []sessionFocusPlan {
	if s.jobs == nil || s.jobs.Meta() == nil {
		return nil
	}
	plans, err := s.jobs.Meta().ListPlans(jobstore.PlanFilter{
		SupervisorSessionID: sid,
		Statuses:            []string{jobstore.PlanOpen, jobstore.PlanBlocked},
		Limit:               sessionFocusPlanLimit,
	})
	if err != nil {
		return nil
	}
	out := make([]sessionFocusPlan, 0, len(plans))
	for _, p := range plans {
		item := sessionFocusPlan{PlanID: p.PlanID, Title: p.Title, Status: p.Status, Blocked: p.BlockedTodo}
		if todos, err := s.jobs.Meta().ListTodosByPlan(p.PlanID); err == nil {
			item.Total = len(todos)
			for _, t := range todos {
				if t.Done {
					continue
				}
				if item.Next == "" {
					item.Next = t.Title
				}
			}
			item.Progress = item.Total - countUndone(todos)
		}
		item.Progress = max(item.Progress, p.Progress)
		out = append(out, item)
	}
	return out
}

func countUndone(todos []jobstore.PlanTodo) int {
	n := 0
	for _, t := range todos {
		if !t.Done {
			n++
		}
	}
	return n
}

// sessionFocusMail lists the letters the session has been given but not answered.
func (s *Server) sessionFocusMail(sid string) []sessionFocusMail {
	if s.jobs == nil || s.jobs.Meta() == nil {
		return nil
	}
	rows, err := s.jobs.Meta().ListMail(jobstore.ListMailOpts{
		ToKind: jobstore.MailKindSession, ToID: sid,
		States: []string{jobstore.MailDelivered},
		Limit:  sessionFocusMailLimit,
	})
	if err != nil {
		return nil
	}
	out := make([]sessionFocusMail, 0, len(rows))
	for _, m := range rows {
		out = append(out, sessionFocusMail{
			ID: m.ID, From: mail.OriginOf(m).Display(),
			Subject: truncateRunes(oneLine(m.Body), sessionFocusBodyRunes), CreatedAt: m.CreatedAt,
		})
	}
	return out
}

// oneLine collapses a letter body onto its first non-empty line.
func oneLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func truncateRunes(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
