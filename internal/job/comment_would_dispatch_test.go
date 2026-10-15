package job

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestCommentWouldDispatch (plan T1.5a): the pure dry-run seam answers what `Comment` would
// dispatch — a comment whose mention resolves and is allowed in the target's project — and it
// writes NOTHING: no comment row, no event, no job. The throttle is deliberately outside it, which
// the last block pins: a comment the interval gate keeps from dispatching still answers as "would
// dispatch", so a caller cannot dodge the permission question by throttling.
func TestCommentWouldDispatch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// The interval gate is ON, so the last block has a throttle that really refuses.
	s := newCommentService(t, root, func(cfg *config.Config) {
		cfg.Server.CommentTrigger = config.CommentTriggerConfig{MinIntervalSec: commentIntPtr(3600)}
	})
	src := commentSourceJob(t, s)

	when := func(author, kind, body string) []string {
		t.Helper()
		mentions, err := s.CommentWouldDispatch(jobstore.CommentScopeJob, src.ID, author, kind, body)
		if err != nil {
			t.Fatalf("CommentWouldDispatch(%q): %v", body, err)
		}
		return mentions
	}

	if got := when("alice", jobstore.CommentAuthorUser, "看起来没问题"); len(got) != 0 {
		t.Fatalf("a plain comment would dispatch %v, want nothing", got)
	}
	if got := when("alice", jobstore.CommentAuthorUser, "@ok 补上测试"); len(got) != 1 || got[0] != "ok" {
		t.Fatalf("the mention would dispatch %v, want [ok]", got)
	}
	// A role resolves to its agent; a mention nothing can dispatch (an unknown name, and one the
	// project does not allow) is not a dispatch.
	if got := when("alice", jobstore.CommentAuthorUser, "@reviewer 看一眼"); len(got) != 1 || got[0] != "reviewer" {
		t.Fatalf("the role mention would dispatch %v, want [reviewer]", got)
	}
	if got := when("alice", jobstore.CommentAuthorUser, "@ghost @reviewer 看"); len(got) != 1 || got[0] != "reviewer" {
		t.Fatalf("an unknown mention would dispatch %v, want only [reviewer]", got)
	}
	// An agent comment that is not the plan's leader dispatches nothing (the 阶段 B author gate).
	if got := when("ok", jobstore.CommentAuthorAgent, "@ok 我自己"); len(got) != 0 {
		t.Fatalf("an agent's mention would dispatch %v, want nothing", got)
	}
	// An unusable target is an ERROR, not an empty answer: the caller keeps its own 404/400.
	if _, err := s.CommentWouldDispatch(jobstore.CommentScopeJob, "job-nope", "alice", jobstore.CommentAuthorUser, "@ok hi"); err == nil {
		t.Fatal("an unknown target must answer an error, not an empty dispatch list")
	}

	// Nothing was written: the thread is empty and no comment event exists on the job.
	rows, err := s.ListComments(jobstore.CommentScopeJob, src.ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("the dry run wrote %d comment(s): %+v", len(rows), rows)
	}
	if types := commentEvents(t, s, src.ID); hasEvent(types, EventCommentCreated) {
		t.Fatalf("the dry run recorded comment events: %v", types)
	}

	// The throttle is NOT part of the question. The first dispatching comment passes (the interval
	// gate counts from the previous one), the second is refused by it — and the dry run answers
	// 「would dispatch」 for both.
	if _, dispatched, err := s.Comment(jobstore.CommentScopeJob, src.ID, "alice", jobstore.CommentAuthorUser, "@ok 先派一个"); err != nil || len(dispatched) != 1 {
		t.Fatalf("the first mention dispatched %+v (err %v), want exactly one job", dispatched, err)
	}
	if _, dispatched, err := s.Comment(jobstore.CommentScopeJob, src.ID, "alice", jobstore.CommentAuthorUser, "@ok 再来一个"); err != nil || len(dispatched) != 0 {
		t.Fatalf("the throttled mention dispatched %+v (err %v), want nothing", dispatched, err)
	}
	if got := when("alice", jobstore.CommentAuthorUser, "@ok 再来一个"); len(got) != 1 || got[0] != "ok" {
		t.Fatalf("a throttled-by-interval mention would dispatch %v, want [ok]", got)
	}
}
