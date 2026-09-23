package jobstore

import (
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

// TestCommentCRUD pins the comment row's own lifecycle (MCP-05 阶段 A): insert with
// a caller-supplied id, read by id, list by scope in CREATED_AT order (a comment
// imported late must still list where it belongs in time), and the
// triggered_job_id back-write that links a comment to the job it dispatched.
func TestCommentCRUD(t *testing.T) {
	s := openTest(t)

	first := Comment{ID: "cm-1", Scope: CommentScopeJob, ScopeID: "job-1", Author: "alice",
		AuthorKind: CommentAuthorUser, Body: "@omp 补上测试", MentionsJSON: `["omp"]`, CreatedAt: 100}
	// Same scope, EARLIER created_at, inserted after — the list must reorder it.
	earlier := Comment{ID: "cm-2", Scope: CommentScopeJob, ScopeID: "job-1", Author: "alice",
		AuthorKind: CommentAuthorUser, Body: "先看这个", CreatedAt: 50}
	planCm := Comment{ID: "cm-3", Scope: CommentScopePlan, ScopeID: "plan-1", Author: "bob",
		AuthorKind: CommentAuthorAgent, Body: "计划进度呢", CreatedAt: 200}
	otherScope := Comment{ID: "cm-4", Scope: CommentScopeJob, ScopeID: "job-2", Author: "bob",
		AuthorKind: CommentAuthorSystem, Body: "系统说明", CreatedAt: 10}
	for _, c := range []Comment{first, earlier, planCm, otherScope} {
		assert.NoErr(t, s.InsertComment(c))
	}
	// The cm-<8hex> grammar belongs to the caller; the store refuses an anonymous row.
	assert.Err(t, s.InsertComment(Comment{Scope: CommentScopeJob, ScopeID: "job-1", Body: "x"}))
	assert.Err(t, s.InsertComment(Comment{ID: "cm-x", Scope: "bogus", ScopeID: "job-1", Body: "x"}))

	got, ok, err := s.GetComment("cm-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "@omp 补上测试", got.Body)
	assert.Eq(t, `["omp"]`, got.MentionsJSON)
	assert.Eq(t, CommentAuthorUser, got.AuthorKind)
	assert.Eq(t, int64(0), got.TriggeredJobID)

	_, ok, err = s.GetComment("cm-nope")
	assert.NoErr(t, err)
	assert.False(t, ok)

	// Scope filter + created_at ordering.
	list, err := s.ListComments(CommentScopeJob, "job-1")
	assert.NoErr(t, err)
	assert.Eq(t, 2, len(list))
	assert.Eq(t, "cm-2", list[0].ID)
	assert.Eq(t, "cm-1", list[1].ID)
	// A sibling scope keeps its own thread.
	list, err = s.ListComments(CommentScopeJob, "job-2")
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(list))
	assert.Eq(t, "cm-4", list[0].ID)
	// An empty thread is an empty slice, not an error.
	list, err = s.ListComments(CommentScopePlan, "plan-none")
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(list))

	// Back-write of the dispatched job id.
	ok, err = s.SetCommentTriggeredJob("cm-1", "job-9")
	assert.NoErr(t, err)
	assert.True(t, ok)
	got, _, err = s.GetComment("cm-1")
	assert.NoErr(t, err)
	assert.Eq(t, "job-9", got.TriggeredJobID)
	ok, err = s.SetCommentTriggeredJob("cm-nope", "job-9")
	assert.NoErr(t, err)
	assert.False(t, ok)

	// Trigger stats drive the throttle: one dispatching comment so far, at its own
	// created_at.
	n, lastAt, err := s.CommentTriggerStats(CommentScopeJob, "job-1")
	assert.NoErr(t, err)
	assert.Eq(t, 1, n)
	assert.Eq(t, int64(100), lastAt)
	// A scope that never dispatched reports zero and no timestamp.
	n, lastAt, err = s.CommentTriggerStats(CommentScopePlan, "plan-1")
	assert.NoErr(t, err)
	assert.Eq(t, 0, n)
	assert.Eq(t, int64(0), lastAt)
}

// TestCommentsPrunedWithJob: comments belong to the thing they are written on, so a
// pruned job takes its thread with it — and only its own. A plan's or a todo's thread
// is swept by the same scope delete (DeleteTodo calls it; plans are never deleted in
// this product — see DeleteCommentsForScope).
func TestCommentsPrunedWithJob(t *testing.T) {
	s := openTest(t)
	now := time.Now().Unix()

	assert.NoErr(t, s.UpsertJob(termJob("job-old", "done", now-100000, now-10*24*3600)))
	assert.NoErr(t, s.UpsertJob(termJob("job-keep", "done", now-50000, now-3600)))
	for _, c := range []Comment{
		{ID: "cm-old", Scope: CommentScopeJob, ScopeID: "job-old", Author: "alice",
			AuthorKind: CommentAuthorUser, Body: "旧 job 上的评论", CreatedAt: now - 100000},
		{ID: "cm-keep", Scope: CommentScopeJob, ScopeID: "job-keep", Author: "alice",
			AuthorKind: CommentAuthorUser, Body: "新 job 上的评论", CreatedAt: now - 3600},
		{ID: "cm-plan", Scope: CommentScopePlan, ScopeID: "plan-1", Author: "alice",
			AuthorKind: CommentAuthorUser, Body: "计划上的评论", CreatedAt: now - 100},
		{ID: "cm-todo", Scope: CommentScopeTodo, ScopeID: "todo-1", Author: "alice",
			AuthorKind: CommentAuthorUser, Body: "待办上的评论", CreatedAt: now - 100},
	} {
		assert.NoErr(t, s.InsertComment(c))
	}

	deleted, _, err := s.PruneJobs(RetentionPolicy{MaxAge: 24 * time.Hour}, now)
	assert.NoErr(t, err)
	assert.Eq(t, 1, deleted)

	old, err := s.ListComments(CommentScopeJob, "job-old")
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(old))
	keep, err := s.ListComments(CommentScopeJob, "job-keep")
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(keep))
	assert.Eq(t, "cm-keep", keep[0].ID)
	// The other scopes are untouched by a job prune.
	for _, tc := range []struct{ scope, id string }{
		{CommentScopePlan, "plan-1"}, {CommentScopeTodo, "todo-1"},
	} {
		rows, err := s.ListComments(tc.scope, tc.id)
		assert.NoErr(t, err)
		assert.Eq(t, 1, len(rows))
	}

	// The scope sweep a plan/todo delete uses: it removes ONE thread and leaves the
	// rest alone (DeleteTodo is the live caller).
	n, err := s.DeleteCommentsForScope(CommentScopePlan, "plan-1")
	assert.NoErr(t, err)
	assert.Eq(t, int64(1), n)
	n, err = s.DeleteCommentsForScope(CommentScopePlan, "plan-1")
	assert.NoErr(t, err)
	assert.Eq(t, int64(0), n)
	rows, err := s.ListComments(CommentScopeTodo, "todo-1")
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(rows))
}

// TestDeleteTodoSweepsItsComments: `plan rm-todo` must not leave a thread pointing at
// a todo that no longer exists.
func TestDeleteTodoSweepsItsComments(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertPlan(Plan{PlanID: "plan-1", Title: "p", Status: PlanOpen}))
	assert.NoErr(t, s.InsertTodo(PlanTodo{TodoID: "todo-1", PlanID: "plan-1", Title: "t"}))
	assert.NoErr(t, s.InsertComment(Comment{ID: "cm-1", Scope: CommentScopeTodo, ScopeID: "todo-1",
		Author: "alice", AuthorKind: CommentAuthorUser, Body: "hi", CreatedAt: 1}))

	ok, err := s.DeleteTodo("todo-1")
	assert.NoErr(t, err)
	assert.True(t, ok)

	rows, err := s.ListComments(CommentScopeTodo, "todo-1")
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(rows))
}
