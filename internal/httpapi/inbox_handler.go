package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/gookit/rux/v2"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/inbox"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/today"
)

type inboxResponse struct {
	Columns     map[string][]inbox.Item `json:"columns"`
	Unread      map[string]int          `json:"unread"`
	TotalUnread int                     `json:"total_unread"`
	GeneratedAt int64                   `json:"generated_at"`
}

func (s *Server) inboxCaller(c *rux.Context) (string, bool) {
	if callerKindFromCtx(c) != callerKindUser {
		writeError(c, http.StatusForbidden, "inbox requires a user caller", "worker and job credentials cannot access personal inbox state")
		return "", false
	}
	id := callerFromCtx(c)
	if strings.TrimSpace(id) == "" {
		writeError(c, http.StatusForbidden, "inbox requires a caller", "personal inbox state requires an authenticated caller id")
		return "", false
	}
	return id, true
}

// inboxConfig returns the effective inbox policy; nil means "built-in defaults, enabled".
func (s *Server) inboxConfig() *config.InboxConfig {
	if cfg := s.serverCfg(); cfg != nil {
		return cfg.Inbox
	}
	return nil
}

// inboxReady runs the checks shared by both inbox routes: a user caller, a wired store and
// an enabled projection. server.inbox.enabled=false answers 404 (the feature is absent, not
// empty) so a client does not mistake "switched off" for "nothing to do".
func (s *Server) inboxReady(c *rux.Context, needToday bool) (string, bool) {
	caller, ok := s.inboxCaller(c)
	if !ok {
		return "", false
	}
	if s.jobs == nil || s.jobs.Meta() == nil || (needToday && s.today == nil) {
		writeError(c, http.StatusServiceUnavailable, "inbox unavailable", "no job store wired on this server")
		return "", false
	}
	if !s.inboxConfig().IsEnabled() {
		writeError(c, http.StatusNotFound, "inbox disabled", "server.inbox.enabled is false")
		return "", false
	}
	return caller, true
}

// GET /v1/inbox returns the current two-column projection and caller-local state.
// total_unread is the top-bar badge and counts the "action" column only; unread.fyi is
// reported separately.
func (s *Server) handleInbox(c *rux.Context) {
	caller, ok := s.inboxReady(c, true)
	if !ok {
		return
	}
	current, err := s.currentInboxItems(c)
	if err != nil {
		writeError(c, 500, "build inbox failed", err.Error())
		return
	}
	states, err := s.jobs.Meta().ListInboxItemStates(caller)
	if err != nil {
		writeError(c, 500, "read inbox state failed", err.Error())
		return
	}
	out := inboxResponse{Columns: map[string][]inbox.Item{"action": {}, "fyi": {}}, Unread: map[string]int{"action": 0, "fyi": 0}, GeneratedAt: time.Now().Unix()}
	for _, item := range current {
		if st, exists := states[item.Key]; exists && st.SourceRevision == item.SourceRevision {
			item.State = inboxState(st)
			if item.State == "cleared" && c.Query("include_cleared") != "1" {
				continue
			}
		}
		if item.State == "unread" {
			out.Unread[item.Column]++
		}
		out.Columns[item.Column] = append(out.Columns[item.Column], item)
	}
	out.TotalUnread = out.Unread[inbox.ColumnAction]
	c.JSON(http.StatusOK, out)
}

// terminalJobKind classifies a finished job for the inbox.
//
//   - done: job.terminal (fyi).
//   - cancelled: job.terminal (fyi); somebody stopped it on purpose, nothing to decide.
//   - failed / timeout that was already continued (AutoResumedBy, FellBackTo, or another job
//     lists it as SourceJobID): job.terminal (fyi); the follow-up carries the decision.
//   - failed / timeout with no continuation: job.failed (action); a person decides whether to
//     resume or retry. Whether the failed job itself was derived from another one is
//     irrelevant: a failed continuation with no successor still needs the decision.
//
// continued is the set of job ids that some other job names as its source.
func terminalJobKind(j jobstore.JobRecord, continued map[string]bool) string {
	if j.Status != job.StatusFailed && j.Status != job.StatusTimeout {
		return "job.terminal"
	}
	if j.AutoResumedBy != "" || j.FellBackTo != "" || continued[j.ID] {
		return "job.terminal"
	}
	return "job.failed"
}

func terminalCard(j jobstore.JobRecord, kind string) today.Card {
	title := j.ID
	if j.Error != "" {
		title = j.ID + " 失败"
	}
	// The key is the same for both kinds so a job's read/cleared state follows it when a
	// follow-up job turns an action item into an fyi one (the revision changes, so it resurfaces).
	return today.Card{Key: "job.terminal:" + j.ID, Kind: kind, Title: title, Summary: j.Error, ProjectKey: j.ProjectKey, ActivityAt: j.EndedAt, WaitingSince: j.EndedAt, Refs: today.Refs{JobID: j.ID}}
}

func todayQueryForInbox(s *Server, c *rux.Context) (q today.Query) {
	q.IncludeExec = queryBool(c, "include_exec")
	if s.ceilingActive(c) {
		q.ProjectAllowed = s.scopeFilter(c)
		q.Projects = s.scopedProjectKeys(q.ProjectAllowed)
	}
	return
}

// currentInboxItems builds the caller-visible projection without personal state, in
// display order (Today's decision order first, then terminal jobs newest first).
func (s *Server) currentInboxItems(c *rux.Context) ([]inbox.Item, error) {
	ic := s.inboxConfig()
	resp, err := s.today.Today(todayQueryForInbox(s, c))
	if err != nil {
		return nil, err
	}
	out := make([]inbox.Item, 0, len(resp.Decisions))
	for _, card := range resp.Decisions {
		if item, ok := inbox.NewItem(card, ic, inbox.State{}); ok {
			out = append(out, item)
		}
	}
	rows, err := s.jobs.Meta().ListJobs(jobstore.ListQuery{Limit: 50})
	if err != nil {
		return nil, err
	}
	continued := map[string]bool{}
	for _, j := range rows {
		if j.SourceJobID != "" {
			continued[j.SourceJobID] = true
		}
	}
	for _, j := range rows {
		if j.EndedAt == 0 || (j.Status != job.StatusDone && j.Status != job.StatusFailed && j.Status != job.StatusCancelled && j.Status != job.StatusTimeout) {
			continue
		}
		if s.ceilingActive(c) && !s.scopeFilter(c)(j.ProjectKey) {
			continue
		}
		if item, ok := inbox.NewItem(terminalCard(j, terminalJobKind(j, continued)), ic, inbox.State{}); ok {
			out = append(out, item)
		}
	}
	return out, nil
}

func inboxState(st jobstore.InboxItemState) string {
	if st.ClearedAt != nil {
		return "cleared"
	}
	if st.SeenAt != nil {
		return "seen"
	}
	return "unread"
}

// POST /v1/inbox/state {action, keys:[...], column?}
//
// seen / clear / restore apply to the listed keys. Without keys, seen and clear apply to
// every current item of the caller's inbox (limited to column when given) — the bulk
// "mark all read" / "clear all" buttons. A batch is all-or-nothing on key validity: when
// any key is not in the caller's current inbox the whole request is 404 and nothing is
// written, so the stored state always matches the response.
func (s *Server) handleInboxState(c *rux.Context) {
	caller, ok := s.inboxReady(c, true)
	if !ok {
		return
	}
	var req struct {
		Action string   `json:"action"`
		Keys   []string `json:"keys"`
		Column string   `json:"column"`
	}
	if err := c.BindJSON(&req); err != nil {
		writeError(c, 400, "invalid request body", err.Error())
		return
	}
	if req.Action != "seen" && req.Action != "clear" && req.Action != "restore" {
		writeError(c, 400, "invalid inbox action", "action must be seen, clear, or restore")
		return
	}
	if req.Column != "" && req.Column != inbox.ColumnAction && req.Column != inbox.ColumnFYI {
		writeError(c, 400, "invalid inbox column", "column must be action or fyi")
		return
	}
	bulk := len(req.Keys) == 0
	if bulk && req.Action == "restore" {
		writeError(c, 400, "empty item keys", "keys must contain at least one item key")
		return
	}
	current, err := s.currentInboxItems(c)
	if err != nil {
		writeError(c, 500, "build inbox failed", err.Error())
		return
	}
	byKey := make(map[string]inbox.Item, len(current))
	for _, item := range current {
		byKey[item.Key] = item
	}
	var targets []inbox.Item
	if bulk {
		states, err := s.jobs.Meta().ListInboxItemStates(caller)
		if err != nil {
			writeError(c, 500, "read inbox state failed", err.Error())
			return
		}
		for _, item := range current {
			if req.Column != "" && item.Column != req.Column {
				continue
			}
			if st, exists := states[item.Key]; exists && st.SourceRevision == item.SourceRevision {
				if st.ClearedAt != nil || (req.Action == "seen" && st.SeenAt != nil) {
					continue
				}
			}
			targets = append(targets, item)
		}
	} else {
		for _, key := range req.Keys {
			if strings.TrimSpace(key) == "" {
				continue
			}
			item, exists := byKey[key]
			if !exists {
				writeError(c, http.StatusNotFound, "inbox item not found", key)
				return
			}
			targets = append(targets, item)
		}
	}
	for _, item := range targets {
		if err := s.jobs.Meta().SetInboxItemState(caller, item.Key, item.Column, item.SourceRevision, req.Action, time.Now()); err != nil {
			writeError(c, 500, "write inbox state failed", err.Error())
			return
		}
	}
	c.JSON(http.StatusOK, map[string]any{"updated": len(targets), "action": req.Action})
}
