package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// Comment scopes: what a comment is written ON. A comment belongs to exactly one
// job, plan or plan-todo (MCP-05 阶段 A); `scope_id` names the object.
const (
	CommentScopeJob  = "job"
	CommentScopePlan = "plan"
	CommentScopeTodo = "todo"
)

// Comment author kinds. The kind is decided by the ENTRY that records the comment,
// never by the comment body: a human caller writes `user`, an agent speaking through
// MCP writes `agent`, and gofer's own explanation (an undispatchable @mention, say)
// writes `system`. The dispatch gate keys on it — only `user` starts work in 阶段 A.
const (
	CommentAuthorUser   = "user"
	CommentAuthorAgent  = "agent"
	CommentAuthorSystem = "system"
)

// Comment is one row of the comments thread (MCP-05 阶段 A). It is a neutral struct
// like JobRecord/PlanTodo so the table can be written without this package importing
// internal/job (see the package doc): MentionsJSON is the opaque JSON array of the
// @names the body mentioned, and TriggeredJobID links the comment to the job it
// started ("" = it started none).
type Comment struct {
	ID         string
	Scope      string
	ScopeID    string
	Author     string
	AuthorKind string
	Body       string
	// MentionsJSON is the parsed @-mention list as JSON (e.g. `["omp"]`), or "" when
	// the body mentioned nobody. It is what the UI highlights and what an audit reads
	// without re-parsing the body.
	MentionsJSON string
	CreatedAt    int64
	// TriggeredJobID is the job this comment dispatched ("" = none). A comment that
	// mentioned several agents links the FIRST one here; the full set travels in the
	// caller's return value and in the `comment.triggered` events.
	TriggeredJobID string
}

// selectCommentCols is the shared projection for the comment reads. COALESCE guards
// the nullable mentions/trigger columns so a NULL scans into "".
const selectCommentCols = `SELECT id, scope, scope_id, author, author_kind, body,
  COALESCE(mentions_json,''), created_at, COALESCE(triggered_job_id,'')
  FROM comments`

func scanComment(sc rowScanner) (Comment, error) {
	var c Comment
	err := sc.Scan(&c.ID, &c.Scope, &c.ScopeID, &c.Author, &c.AuthorKind, &c.Body,
		&c.MentionsJSON, &c.CreatedAt, &c.TriggeredJobID)
	return c, err
}

// ValidCommentScope reports whether scope names a commentable object kind.
func ValidCommentScope(scope string) bool {
	switch scope {
	case CommentScopeJob, CommentScopePlan, CommentScopeTodo:
		return true
	}
	return false
}

// ValidCommentAuthorKind reports whether kind is one of the three author kinds.
func ValidCommentAuthorKind(kind string) bool {
	switch kind {
	case CommentAuthorUser, CommentAuthorAgent, CommentAuthorSystem:
		return true
	}
	return false
}

// InsertComment persists one comment. It is the single收口 point for the invariants
// every entry face relies on: the ID is caller-supplied (the `cm-<8hex>` grammar
// belongs to the caller, like every other id in this package), the scope must be one
// of the three commentable kinds, the author must be non-empty and the author kind
// must be one of the three (an unknown kind would silently disable the dispatch gate).
func (s *Store) InsertComment(c Comment) error {
	if c.ID == "" {
		return errors.New("jobstore: InsertComment: empty comment id")
	}
	if !ValidCommentScope(c.Scope) {
		return fmt.Errorf("jobstore: InsertComment %q: invalid scope %q", c.ID, c.Scope)
	}
	if c.ScopeID == "" {
		return fmt.Errorf("jobstore: InsertComment %q: empty scope_id", c.ID)
	}
	if c.Author == "" {
		return fmt.Errorf("jobstore: InsertComment %q: empty author", c.ID)
	}
	if !ValidCommentAuthorKind(c.AuthorKind) {
		return fmt.Errorf("jobstore: InsertComment %q: invalid author kind %q", c.ID, c.AuthorKind)
	}
	if c.Body == "" {
		return fmt.Errorf("jobstore: InsertComment %q: empty body", c.ID)
	}
	var mentions, triggered any
	if c.MentionsJSON != "" {
		mentions = c.MentionsJSON
	}
	if c.TriggeredJobID != "" {
		triggered = c.TriggeredJobID
	}
	const q = `INSERT INTO comments
  (id, scope, scope_id, author, author_kind, body, mentions_json, created_at, triggered_job_id)
  VALUES (?,?,?,?,?,?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q, c.ID, c.Scope, c.ScopeID, c.Author, c.AuthorKind, c.Body,
		mentions, c.CreatedAt, triggered); err != nil {
		return fmt.Errorf("jobstore: insert comment %q: %w", c.ID, err)
	}
	return nil
}

// GetComment returns a comment by id. ok is false with nil error when absent.
func (s *Store) GetComment(id string) (Comment, bool, error) {
	c, err := scanComment(s.db.QueryRow(selectCommentCols+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Comment{}, false, nil
	}
	if err != nil {
		return Comment{}, false, fmt.Errorf("jobstore: get comment %q: %w", id, err)
	}
	return c, true, nil
}

// ListComments returns one thread (scope + scope_id) in created_at order, id as the
// tiebreaker so two comments written in the same second keep a stable order.
func (s *Store) ListComments(scope, scopeID string) ([]Comment, error) {
	if !ValidCommentScope(scope) {
		return nil, fmt.Errorf("jobstore: list comments: invalid scope %q", scope)
	}
	rows, err := s.db.Query(selectCommentCols+" WHERE scope = ? AND scope_id = ? ORDER BY created_at ASC, id ASC",
		scope, scopeID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list comments of %s %q: %w", scope, scopeID, err)
	}
	defer rows.Close()
	out := make([]Comment, 0)
	for rows.Next() {
		c, scanErr := scanComment(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan comment row: %w", scanErr)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list comments of %s %q rows: %w", scope, scopeID, err)
	}
	return out, nil
}

// SetCommentTriggeredJob records the job a comment dispatched. It is a conditional
// UPDATE on the row id, so a comment already linked is simply overwritten (the last
// dispatch wins) and an unknown id reports ok=false rather than an error.
func (s *Store) SetCommentTriggeredJob(id, jobID string) (bool, error) {
	if jobID == "" {
		return false, errors.New("jobstore: SetCommentTriggeredJob: empty job id")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec("UPDATE comments SET triggered_job_id = ? WHERE id = ?", jobID, id)
	if err != nil {
		return false, fmt.Errorf("jobstore: set comment %q triggered job: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: set comment %q rows: %w", id, err)
	}
	return n == 1, nil
}

// CommentTriggerStats reports how many comments in a thread have DISPATCHED a job and
// when the most recent one was written — the two numbers the @-mention throttle needs
// (MCP-05 阶段 A: one dispatching comment per scope per interval, and a cumulative cap
// per scope). A comment that mentioned several agents counts ONCE: the budget counts
// dispatching comments, and the mention fan-out inside one comment is already bounded
// by the mention list itself.
func (s *Store) CommentTriggerStats(scope, scopeID string) (triggered int, lastAt int64, err error) {
	if !ValidCommentScope(scope) {
		return 0, 0, fmt.Errorf("jobstore: comment trigger stats: invalid scope %q", scope)
	}
	row := s.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(created_at),0) FROM comments
  WHERE scope = ? AND scope_id = ? AND triggered_job_id IS NOT NULL`, scope, scopeID)
	if err := row.Scan(&triggered, &lastAt); err != nil {
		return 0, 0, fmt.Errorf("jobstore: comment trigger stats of %s %q: %w", scope, scopeID, err)
	}
	return triggered, lastAt, nil
}

// DeleteCommentsForScope removes one object's whole thread and reports how many rows
// went. It is the cascade a job/plan/todo delete runs (PruneJobs and DeleteTodo call
// it); plans are never deleted in this product, so a plan thread lives as long as its
// plan.
func (s *Store) DeleteCommentsForScope(scope, scopeID string) (int64, error) {
	if !ValidCommentScope(scope) {
		return 0, fmt.Errorf("jobstore: delete comments: invalid scope %q", scope)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec("DELETE FROM comments WHERE scope = ? AND scope_id = ?", scope, scopeID)
	if err != nil {
		return 0, fmt.Errorf("jobstore: delete comments of %s %q: %w", scope, scopeID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("jobstore: delete comments of %s %q rows: %w", scope, scopeID, err)
	}
	return n, nil
}

// deleteCommentsTx is the in-transaction cascade the prune paths use (they already
// hold writeMu and a tx). A failure is reported to the caller, which rolls back.
func deleteCommentsTx(tx *sql.Tx, scope, scopeID string) error {
	if _, err := tx.Exec("DELETE FROM comments WHERE scope = ? AND scope_id = ?", scope, scopeID); err != nil {
		return fmt.Errorf("jobstore: delete comments of %s %q: %w", scope, scopeID, err)
	}
	return nil
}
