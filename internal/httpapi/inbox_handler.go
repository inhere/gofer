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
	// RulesVersion changes when the effective rules change (see GET /v1/inbox/rules).
	RulesVersion string `json:"rules_version"`
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
	out := inboxResponse{Columns: map[string][]inbox.Item{"action": {}, "fyi": {}}, Unread: map[string]int{"action": 0, "fyi": 0}, GeneratedAt: time.Now().Unix(), RulesVersion: inbox.RulesVersion(s.inboxConfig())}
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

// GET /v1/inbox/rules is the rule directory: effective and built-in rules, per kind the
// default and effective column with their source, and the notify events that announce each
// kind. It is global configuration, not personal state, so it answers even when the inbox is
// switched off (enabled=false in the body) and needs no job store.
func (s *Server) handleInboxRules(c *rux.Context) {
	if _, ok := s.inboxCaller(c); !ok {
		return
	}
	c.JSON(http.StatusOK, inbox.Directory(s.inboxConfig()))
}

// waitingFor is the one 「等我」 list of a request: the structured-fact cards of the today queue
// (cards: today.Response.Cards, built only from store rows) plus the caller-visible failures of
// jobs a person submitted (inbox.Waiting / inbox.TerminalCards).
// The home page, the inbox's action column and the top-bar badge all read what this returns,
// so they cannot disagree. fyi is the person's own finished jobs (the inbox's second column).
func (s *Server) waitingFor(c *rux.Context, queue []today.Card) (waiting, fyi []today.Card, err error) {
	var failed []today.Card
	if s.jobs != nil && s.jobs.Meta() != nil {
		var allow func(string) bool
		if s.ceilingActive(c) {
			allow = s.scopeFilter(c)
		}
		if failed, fyi, err = inbox.TerminalCards(s.jobs.Meta(), s.requestLang(c), allow); err != nil {
			return nil, nil, err
		}
	}
	cards := make([]today.Card, 0, len(queue)+len(failed))
	cards = append(append(cards, queue...), failed...)
	return inbox.Waiting(s.requestLang(c), cards), fyi, nil
}

func todayQueryForInbox(s *Server, c *rux.Context) (q today.Query) {
	q.IncludeExec = queryBool(c, "include_exec")
	q.Lang = s.requestLang(c)
	if s.ceilingActive(c) {
		q.ProjectAllowed = s.scopeFilter(c)
		q.Projects = s.scopedProjectKeys(q.ProjectAllowed)
	}
	return
}

// currentInboxItems builds the caller-visible projection without personal state, in display
// order: the 「等我」 list (inbox.Waiting) first, then the person's own finished jobs newest
// first.
func (s *Server) currentInboxItems(c *rux.Context) ([]inbox.Item, error) {
	ic := s.inboxConfig()
	resp, err := s.today.Today(todayQueryForInbox(s, c))
	if err != nil {
		return nil, err
	}
	waiting, fyi, err := s.waitingFor(c, resp.Cards)
	if err != nil {
		return nil, err
	}
	out := make([]inbox.Item, 0, len(waiting)+len(fyi))
	for _, card := range append(waiting, fyi...) {
		if item, ok := inbox.NewItem(card, ic, inbox.State{}); ok {
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
