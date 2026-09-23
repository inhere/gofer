package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// commentAnonymousAuthor is the author stamped on a comment posted through a server
// running with allow_empty_token, where the auth middleware has no caller id to give
// (callerFromCtx returns ""). A comment must say who wrote it — the dispatch gate is
// keyed on the author kind — so an unidentified caller is named, not left blank.
const commentAnonymousAuthor = "anonymous"

// commentReq is the POST body of every comment route.
type commentReq struct {
	Body string `json:"body"`
	// AsJob is the retired in-job identity field (MCP-05 阶段 A: the caller declared
	// which job's agent was speaking, and the server resolved it to that job's agent
	// key). Identity now comes from the CREDENTIAL — a job authenticates with its own
	// GOFER_JOB_TOKEN (SEC-01), so there is nothing to declare — and the field is
	// accepted but IGNORED for every caller. It stays in the struct only so an old
	// client that still sends it does not get a 400.
	//
	// DEPRECATED(v0.58): remove in v0.61
	AsJob string `json:"as_job,omitempty"`
}

// commentDispatchView is one job an @-mention started, as the thread renders it.
type commentDispatchView struct {
	Mention string `json:"mention"`
	Kind    string `json:"kind"`
	JobID   string `json:"job_id"`
}

// commentView is one comment row on the wire (MCP-05 阶段 A).
type commentView struct {
	ID         string   `json:"id"`
	Scope      string   `json:"scope"`
	ScopeID    string   `json:"scope_id"`
	Author     string   `json:"author"`
	AuthorKind string   `json:"author_kind"`
	Body       string   `json:"body"`
	Mentions   []string `json:"mentions"`
	CreatedAt  int64    `json:"created_at"`
	// TriggeredJobID is the job this comment started ("" = none). Dispatched carries
	// the full set when one comment mentioned several agents; both are what the CLI,
	// the web thread and MCP show as "→ 已派发 job <id>".
	TriggeredJobID string                `json:"triggered_job_id,omitempty"`
	Dispatched     []commentDispatchView `json:"dispatched,omitempty"`
}

// toCommentView projects a stored comment (+ what it dispatched) onto the wire shape.
// The mention list is decoded from the row's opaque JSON so a client never re-parses
// the body to highlight it.
func toCommentView(cm jobstore.Comment, dispatched []job.CommentDispatch) commentView {
	v := commentView{
		ID: cm.ID, Scope: cm.Scope, ScopeID: cm.ScopeID,
		Author: cm.Author, AuthorKind: cm.AuthorKind, Body: cm.Body,
		Mentions:       decodeMentions(cm.MentionsJSON),
		CreatedAt:      cm.CreatedAt,
		TriggeredJobID: cm.TriggeredJobID,
	}
	for _, d := range dispatched {
		v.Dispatched = append(v.Dispatched, commentDispatchView{Mention: d.Mention, Kind: d.Kind, JobID: d.JobID})
	}
	return v
}

// decodeMentions turns the row's mentions_json into a slice, never nil (the JSON
// contract the web client reads is an empty array, not null).
func decodeMentions(raw string) []string {
	if raw == "" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return []string{}
	}
	return out
}

// commentStatus maps the job package's comment sentinels onto HTTP: an unknown target
// is a 404 and a malformed comment a 400, exactly like the other submit-adjacent
// handlers (no string matching).
func commentStatus(err error) int {
	if errors.Is(err, job.ErrUnknownCommentTarget) {
		return http.StatusNotFound
	}
	if errors.Is(err, job.ErrInvalidComment) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// commentAuthor resolves who is writing for a NON-job caller: the authenticated
// caller id, as a user. A job's comment never comes through here — its author is
// derived from the credential (postComment), which is the whole point of SEC-01.
func (s *Server) commentAuthor(c *rux.Context) (author, kind string) {
	author = callerFromCtx(c)
	if author == "" {
		author = commentAnonymousAuthor
	}
	return author, jobstore.CommentAuthorUser
}

// postComment is the shared body of the three POST routes: gate the caller, bind the
// body, resolve the author, record the comment and report what it dispatched.
func (s *Server) postComment(c *rux.Context, scope, scopeID string) {
	// A comment can start work, and an executing MACHINE (a worker token, which is a
	// transport credential rather than an identity) may read a thread but never speak
	// in one (MCP-05 阶段 A). A JOB caller is a different thing and is allowed: the
	// design's whole point is that a job's agent reports progress through its own
	// credential (SEC-01 §一.3).
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, "comment not permitted for this caller",
			"worker tokens cannot comment: a comment can dispatch work, and a worker token is not an identity")
		return
	}
	var body commentReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if strings.TrimSpace(body.Body) == "" {
		writeError(c, http.StatusBadRequest, "body required", "a comment requires a body")
		return
	}
	// The author is the CREDENTIAL's identity, never the body's (SEC-01): a job caller
	// speaks as that job's agent through CommentAsJob (which also feeds the 阶段 B
	// leader gate the job id it needs — a leader's mention may dispatch inside its own
	// plan, a member's is recorded), and every other caller speaks as itself.
	// body.AsJob is deliberately NOT read; see the field's DEPRECATED note.
	var cm jobstore.Comment
	var dispatched []job.CommentDispatch
	var err error
	if jc, ok := jobCallerFromCtx(c); ok {
		cm, dispatched, err = s.jobs.CommentAsJob(scope, scopeID, jc.JobID, body.Body)
	} else {
		author, kind := s.commentAuthor(c)
		cm, dispatched, err = s.jobs.Comment(scope, scopeID, author, kind, body.Body)
	}
	if err != nil {
		writeError(c, commentStatus(err), "comment failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, toCommentView(cm, dispatched))
}

// listComments is the shared body of the GET routes: an unknown target is a 404 (not
// an empty thread), an empty one an empty array.
func (s *Server) listComments(c *rux.Context, scope, scopeID string) {
	rows, err := s.jobs.ListComments(scope, scopeID)
	if err != nil {
		writeError(c, commentStatus(err), "list comments failed", err.Error())
		return
	}
	out := make([]commentView, 0, len(rows))
	for _, cm := range rows {
		out = append(out, toCommentView(cm, nil))
	}
	c.JSON(http.StatusOK, map[string]any{"comments": out})
}

// --- job scope --------------------------------------------------------------

func (s *Server) handlePostJobComment(c *rux.Context) {
	s.postComment(c, jobstore.CommentScopeJob, c.Param("id"))
}

func (s *Server) handleListJobComments(c *rux.Context) {
	s.listComments(c, jobstore.CommentScopeJob, c.Param("id"))
}

// --- plan scope -------------------------------------------------------------

func (s *Server) handlePostPlanComment(c *rux.Context) {
	s.postComment(c, jobstore.CommentScopePlan, c.Param("id"))
}

func (s *Server) handleListPlanComments(c *rux.Context) {
	s.listComments(c, jobstore.CommentScopePlan, c.Param("id"))
}

// --- todo scope -------------------------------------------------------------

// handlePostTodoComment serves POST /v1/todos/{todo_id}/comments — the id-only form
// the CLI and MCP address a checklist item's thread with (a caller holding a todo id
// needs no plan id to speak about it).
func (s *Server) handlePostTodoComment(c *rux.Context) {
	s.postComment(c, jobstore.CommentScopeTodo, c.Param("todo_id"))
}

func (s *Server) handleListTodoComments(c *rux.Context) {
	s.listComments(c, jobstore.CommentScopeTodo, c.Param("todo_id"))
}

// handlePostPlanTodoComment serves the nested route POST /v1/plans/{id}/todos/{todo_id}/comments.
// The nesting is a CHECK, not a second address: the todo must belong to that plan, so a
// plan page can never write into another plan's item thread by carrying a stale id.
func (s *Server) handlePostPlanTodoComment(c *rux.Context) {
	todoID, ok := s.planTodoOf(c)
	if !ok {
		return
	}
	s.postComment(c, jobstore.CommentScopeTodo, todoID)
}

func (s *Server) handleListPlanTodoComments(c *rux.Context) {
	todoID, ok := s.planTodoOf(c)
	if !ok {
		return
	}
	s.listComments(c, jobstore.CommentScopeTodo, todoID)
}

// planTodoOf resolves the {todo_id} of a plan-nested todo route and verifies it belongs
// to {id}. It writes the 404 and returns ok=false when it does not.
func (s *Server) planTodoOf(c *rux.Context) (string, bool) {
	planID, todoID := c.Param("id"), c.Param("todo_id")
	t, ok, err := s.jobs.Meta().GetTodo(todoID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get todo failed", err.Error())
		return "", false
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown todo", "no todo with id "+todoID)
		return "", false
	}
	if t.PlanID != planID {
		writeError(c, http.StatusNotFound, "unknown todo",
			"todo "+todoID+" does not belong to plan "+planID)
		return "", false
	}
	return todoID, true
}
