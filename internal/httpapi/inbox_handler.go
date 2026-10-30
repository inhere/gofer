package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/gookit/rux/v2"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/inbox"
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

// GET /v1/inbox returns the current two-column projection and caller-local state.
func (s *Server) handleInbox(c *rux.Context) {
	caller, ok := s.inboxCaller(c)
	if !ok {
		return
	}
	if s.today == nil || s.jobs == nil || s.jobs.Meta() == nil {
		writeError(c, http.StatusServiceUnavailable, "inbox unavailable", "no job store wired on this server")
		return
	}
	cfg := s.serverCfg()
	var ic *config.InboxConfig
	if cfg != nil {
		ic = cfg.Inbox
	}
	q := todayQueryForInbox(s, c)
	resp, err := s.today.Today(q)
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
	for _, card := range resp.Decisions {
		item, yes := inbox.NewItem(card, ic, inbox.State{})
		if !yes {
			continue
		}
		if st, exists := states[item.Key]; exists && st.SourceRevision == item.SourceRevision {
			item.State = inboxState(st)
			if item.State == "cleared" && c.Query("include_cleared") != "1" {
				continue
			}
			if item.State != "seen" && item.State != "cleared" {
				out.Unread[item.Column]++
			}
		} else {
			out.Unread[item.Column]++
		}
		out.Columns[item.Column] = append(out.Columns[item.Column], item)
	}
	// Terminal jobs are FYI. A standalone failure is actionable; derived/continued failures are FYI.
	rows, _ := s.jobs.Meta().ListJobs(jobstore.ListQuery{Limit: 50})
	for _, j := range rows {
		if j.EndedAt == 0 || (j.Status != "done" && j.Status != "failed" && j.Status != "cancelled" && j.Status != "timed_out") {
			continue
		}
		if s.ceilingActive(c) && !s.scopeFilter(c)(j.ProjectKey) {
			continue
		}
		col := "fyi"
		if j.Status != "done" && j.SourceJobID == "" {
			col = "action"
		}
		item, ok := inbox.NewItem(terminalCard(j, col), ic, inbox.State{})
		if !ok {
			continue
		}
		if st, exists := states[item.Key]; exists && st.SourceRevision == item.SourceRevision {
			item.State = inboxState(st)
			if item.State == "cleared" && c.Query("include_cleared") != "1" {
				continue
			}
			if item.State == "seen" || item.State == "cleared" {
			} else {
				out.Unread[item.Column]++
			}
		} else {
			out.Unread[item.Column]++
		}
		out.Columns[item.Column] = append(out.Columns[item.Column], item)
	}
	out.TotalUnread = out.Unread["action"] + out.Unread["fyi"]
	c.JSON(http.StatusOK, out)
}

func terminalCard(j jobstore.JobRecord, column string) today.Card {
	title := j.ID
	if j.Error != "" {
		title = j.ID + " 失败"
	}
	return today.Card{Key: "job.terminal:" + j.ID, Kind: "job.terminal", Title: title, Summary: j.Error, ProjectKey: j.ProjectKey, ActivityAt: j.EndedAt, WaitingSince: j.EndedAt, Refs: today.Refs{JobID: j.ID}, Column: column}
}

func todayQueryForInbox(s *Server, c *rux.Context) (q today.Query) {
	q.IncludeExec = queryBool(c, "include_exec")
	if s.ceilingActive(c) {
		q.ProjectAllowed = s.scopeFilter(c)
		q.Projects = s.scopedProjectKeys(q.ProjectAllowed)
	}
	return
}
func (s *Server) currentInboxItems(c *rux.Context) (map[string]inbox.Item, error) {
	cfg := s.serverCfg()
	var ic *config.InboxConfig
	if cfg != nil {
		ic = cfg.Inbox
	}
	resp, err := s.today.Today(todayQueryForInbox(s, c))
	if err != nil {
		return nil, err
	}
	out := make(map[string]inbox.Item, len(resp.Decisions))
	for _, card := range resp.Decisions {
		if item, ok := inbox.NewItem(card, ic, inbox.State{}); ok {
			out[item.Key] = item
		}
	}
	rows, _ := s.jobs.Meta().ListJobs(jobstore.ListQuery{Limit: 50})
	for _, j := range rows {
		if j.EndedAt == 0 || (j.Status != "done" && j.Status != "failed" && j.Status != "cancelled" && j.Status != "timed_out") {
			continue
		}
		if s.ceilingActive(c) && !s.scopeFilter(c)(j.ProjectKey) {
			continue
		}
		col := "fyi"
		if j.Status != "done" && j.SourceJobID == "" {
			col = "action"
		}
		if item, ok := inbox.NewItem(terminalCard(j, col), ic, inbox.State{}); ok {
			out[item.Key] = item
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

// POST /v1/inbox/state {action, keys:[...], column?, expected_revision?}
func (s *Server) handleInboxState(c *rux.Context) {
	caller, ok := s.inboxCaller(c)
	if !ok {
		return
	}
	if s.jobs == nil || s.jobs.Meta() == nil {
		writeError(c, 503, "inbox unavailable", "no job store wired")
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
	if req.Action == "clear" && len(req.Keys) == 0 {
		if err := s.jobs.Meta().ClearInboxItemStates(caller, req.Column, time.Now()); err != nil {
			writeError(c, 500, "clear inbox failed", err.Error())
			return
		}
		c.JSON(http.StatusOK, map[string]any{"updated": 0, "action": req.Action})
		return
	}
	if len(req.Keys) == 0 {
		writeError(c, 400, "empty item keys", "keys must contain at least one item key")
		return
	}
	current, err := s.currentInboxItems(c)
	if err != nil {
		writeError(c, 500, "build inbox failed", err.Error())
		return
	}
	updated := 0
	for _, key := range req.Keys {
		if strings.TrimSpace(key) == "" {
			continue
		}
		item, exists := current[key]
		if !exists {
			writeError(c, http.StatusNotFound, "inbox item not found", key)
			return
		}
		if err := s.jobs.Meta().SetInboxItemState(caller, key, item.Column, item.SourceRevision, req.Action, time.Now()); err != nil {
			writeError(c, 500, "write inbox state failed", err.Error())
			return
		}
		updated++
	}
	c.JSON(http.StatusOK, map[string]any{"updated": updated, "action": req.Action})
}
