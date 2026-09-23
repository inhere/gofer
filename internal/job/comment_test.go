package job

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// newCommentService builds a Service for the comment tests (MCP-05 阶段 A): one
// cli-agent "ok" that echoes its argv (so a dispatched job's prompt is observable),
// one role "reviewer" over it, and two projects — "self" allows both, "locked" only
// the built-in exec (the project-allowlist gate). mutate may override config (e.g.
// the comment_trigger throttle knobs).
func newCommentService(t *testing.T, root string, mutate func(*config.Config)) *Service {
	t.Helper()
	projDir := filepath.Join(root, "work")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: filepath.Join(root, "data")},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       projDir,
				AllowedAgents:  []string{"ok", agent.ExecAgentKey},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
			"locked": {
				HostPath:       projDir,
				AllowedAgents:  []string{agent.ExecAgentKey},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"ok": {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"argv", "{{prompt}}"}},
		},
		Roles: map[string]config.RoleConfig{
			"reviewer": {Agent: "ok"},
		},
	}
	if mutate != nil {
		mutate(cfg)
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	meta, err := jobstore.Open(jobstoreDBPath(root))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	return drainOnClose(t, NewService(cfg, projReg, agentReg, runners, meta, nil))
}

// commentSourceJob runs one real job in "self" (agent "ok", which echoes its argv) and
// returns it — the job a comment is then written on. Its stdout carries the marker, so
// the "汇报尾部摘要" a dispatched job receives is assertable.
func commentSourceJob(t *testing.T, s *Service) JobResult {
	t.Helper()
	return submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local", Cwd: ".",
		Title: "源 job", Prompt: "REPORT-MARKER-77 先把 X 做出来", TimeoutSec: 30,
	})
}

// commentEvents lists the event types recorded on scope.
func commentEvents(t *testing.T, s *Service, scope string) []string {
	t.Helper()
	evs, err := s.ListJobEvents(scope, 0)
	if err != nil {
		t.Fatalf("ListJobEvents(%s): %v", scope, err)
	}
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func hasEvent(types []string, want string) bool {
	for _, t := range types {
		if t == want {
			return true
		}
	}
	return false
}

// eventDetailOf returns the detail_json of the LAST event of the given type.
func commentEventDetail(t *testing.T, s *Service, scope, eventType string) string {
	t.Helper()
	evs, err := s.ListJobEvents(scope, 0)
	if err != nil {
		t.Fatalf("ListJobEvents(%s): %v", scope, err)
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == eventType {
			return evs[i].Detail
		}
	}
	return ""
}

// TestParseMentions pins the mention grammar: @<name> outside code spans/blocks, never
// an e-mail, deduplicated, in first-seen order, with trailing punctuation dropped.
func TestParseMentions(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"plain", "@omp 请补上测试", []string{"omp"}},
		{"dashed name", "@omp-acp 看一下", []string{"omp-acp"}},
		{"role name", "@reviewer 你来", []string{"reviewer"}},
		{"underscore and digits", "@sup_2 go", []string{"sup_2"}},
		{"at line start", "先做这个\n@omp 后做那个", []string{"omp"}},
		{"email is not a mention", "有事邮件 email@x.com 找我", nil},
		{"inline code", "写法是 `@omp` 这样的", nil},
		{"fenced block", "示例：\n```\n@omp 干活\n```\n结束", nil},
		{"fenced block then real mention", "```\n@nobody\n```\n@omp 你上", []string{"omp"}},
		{"dedup keeps first order", "@omp @ok @omp 都来", []string{"omp", "ok"}},
		{"trailing punctuation", "done @omp.", []string{"omp"}},
		{"parenthesised", "(@omp) 看一下", []string{"omp"}},
		{"bare at is not a mention", "ping @ nobody", nil},
		{"name boundary", "@omp/extra", []string{"omp"}},
		{"empty", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseMentions(tc.body)
			if len(got) != len(tc.want) {
				t.Fatalf("ParseMentions(%q) = %v, want %v", tc.body, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ParseMentions(%q) = %v, want %v", tc.body, got, tc.want)
				}
			}
		})
	}
}

// TestCommentMentionSubmitsJob: a USER's comment mentioning a registered agent starts
// a job through the same Submit entry — same project, the source job's cwd, a prompt
// carrying the commented job's context (title/status/report tail) plus the comment
// body, and tags that point back at the comment. The comment row links the new job and
// both events are recorded.
func TestCommentMentionSubmitsJob(t *testing.T) {
	root := t.TempDir()
	s := newCommentService(t, root, nil)
	src := commentSourceJob(t, s)

	cm, dispatched, err := s.Comment(jobstore.CommentScopeJob, src.ID, "alice", jobstore.CommentAuthorUser, "@ok 补上测试")
	if err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if len(dispatched) != 1 {
		t.Fatalf("dispatched = %+v, want exactly one job", dispatched)
	}
	if dispatched[0].Mention != "ok" || dispatched[0].Kind != "agent" {
		t.Fatalf("dispatch = %+v, want mention ok kind agent", dispatched[0])
	}
	newID := dispatched[0].JobID
	if newID == "" || newID == src.ID {
		t.Fatalf("dispatch job id = %q", newID)
	}

	// The comment row is linked to what it started.
	row, ok, err := s.Meta().GetComment(cm.ID)
	if err != nil || !ok {
		t.Fatalf("GetComment(%s): ok=%v err=%v", cm.ID, ok, err)
	}
	if row.TriggeredJobID != newID {
		t.Fatalf("triggered_job_id = %q, want %q", row.TriggeredJobID, newID)
	}
	if row.Scope != jobstore.CommentScopeJob || row.ScopeID != src.ID || row.AuthorKind != jobstore.CommentAuthorUser {
		t.Fatalf("comment row = %+v", row)
	}
	if row.MentionsJSON != `["ok"]` {
		t.Fatalf("mentions_json = %q", row.MentionsJSON)
	}

	final, ok := s.Wait(newID)
	if !ok {
		t.Fatalf("Wait: dispatched job %s not found", newID)
	}
	if final.ProjectKey != src.ProjectKey {
		t.Fatalf("project = %q, want the source job's %q", final.ProjectKey, src.ProjectKey)
	}
	if final.Cwd != src.Cwd {
		t.Fatalf("cwd = %q, want the source job's %q", final.Cwd, src.Cwd)
	}
	var req JobRequest
	if err := json.Unmarshal([]byte(final.RequestJSON), &req); err != nil {
		t.Fatalf("request_json: %v", err)
	}
	if !hasTag(req.Tags, "from_comment") || !hasTag(req.Tags, "comment_of:"+cm.ID) {
		t.Fatalf("tags = %v, want from_comment + comment_of:%s", req.Tags, cm.ID)
	}
	for _, want := range []string{src.Title, src.Status, "REPORT-MARKER-77", "补上测试"} {
		if !strings.Contains(req.Prompt, want) {
			t.Fatalf("prompt is missing %q:\n%s", want, req.Prompt)
		}
	}

	types := commentEvents(t, s, src.ID)
	if !hasEvent(types, EventCommentCreated) || !hasEvent(types, EventCommentTriggered) {
		t.Fatalf("events = %v, want %s + %s", types, EventCommentCreated, EventCommentTriggered)
	}
	if d := commentEventDetail(t, s, src.ID, EventCommentTriggered); !strings.Contains(d, newID) {
		t.Fatalf("comment.triggered detail = %q, want the new job id %q", d, newID)
	}
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// TestCommentOnTodoBindsTodo: a comment on a checklist item dispatches a job bound to
// that todo (so the outcome is written back into the item), and the role mention fills
// the role's agent.
func TestCommentOnTodoBindsTodo(t *testing.T) {
	root := t.TempDir()
	s := newCommentService(t, root, nil)
	if err := s.Meta().InsertPlan(jobstore.Plan{PlanID: "plan-1", Title: "计划", ProjectKey: "self", Status: jobstore.PlanOpen}); err != nil {
		t.Fatalf("InsertPlan: %v", err)
	}
	if err := s.Meta().InsertTodo(jobstore.PlanTodo{TodoID: "todo-1", PlanID: "plan-1", Title: "补测试", Status: jobstore.TodoPending}); err != nil {
		t.Fatalf("InsertTodo: %v", err)
	}

	_, dispatched, err := s.Comment(jobstore.CommentScopeTodo, "todo-1", "alice", jobstore.CommentAuthorUser, "@reviewer 你来做")
	if err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if len(dispatched) != 1 {
		t.Fatalf("dispatched = %+v, want one job", dispatched)
	}
	if dispatched[0].Kind != "role" {
		t.Fatalf("dispatch kind = %q, want role", dispatched[0].Kind)
	}
	final, ok := s.Get(dispatched[0].JobID)
	if !ok {
		t.Fatalf("job %s not found", dispatched[0].JobID)
	}
	if final.TodoID != "todo-1" {
		t.Fatalf("todo_id = %q, want todo-1", final.TodoID)
	}
	if final.PlanID != "plan-1" {
		t.Fatalf("plan_id = %q, want plan-1", final.PlanID)
	}
	// A role mention resolves onto the role's agent at submit; the role name stays
	// recorded so "who was asked" is not lost.
	if final.Agent != "ok" {
		t.Fatalf("agent = %q, want the role's agent ok", final.Agent)
	}

	// The event lands on the PLAN's scope (a todo belongs to a plan), so the plan
	// timeline answers "what did this comment start".
	types := commentEvents(t, s, PlanEventScope("plan-1"))
	if !hasEvent(types, EventCommentTriggered) {
		t.Fatalf("plan events = %v, want %s", types, EventCommentTriggered)
	}
}

// TestAgentCommentDoesNotTrigger: an agent-authored comment (an MCP tool call from
// inside a job) is RECORDED but dispatches nothing — 阶段 A only a human's word starts
// work. The leader whitelist that would extend this is 阶段 B.
func TestAgentCommentDoesNotTrigger(t *testing.T) {
	root := t.TempDir()
	s := newCommentService(t, root, nil)
	src := commentSourceJob(t, s)

	cm, dispatched, err := s.Comment(jobstore.CommentScopeJob, src.ID, "ok", jobstore.CommentAuthorAgent, "@ok 再来一次")
	if err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if len(dispatched) != 0 {
		t.Fatalf("an agent comment dispatched %+v, want none", dispatched)
	}
	row, ok, err := s.Meta().GetComment(cm.ID)
	if err != nil || !ok {
		t.Fatalf("GetComment: ok=%v err=%v", ok, err)
	}
	if row.AuthorKind != jobstore.CommentAuthorAgent || row.Author != "ok" {
		t.Fatalf("comment row = %+v, want author ok/agent", row)
	}
	if row.TriggeredJobID != "" {
		t.Fatalf("triggered_job_id = %q, want empty", row.TriggeredJobID)
	}
	types := commentEvents(t, s, src.ID)
	if !hasEvent(types, EventCommentCreated) {
		t.Fatalf("events = %v, want %s (recorded)", types, EventCommentCreated)
	}
	if hasEvent(types, EventCommentTriggered) {
		t.Fatalf("events = %v, want NO %s", types, EventCommentTriggered)
	}
}

// TestMentionThrottled: the two gates that keep @-dispatch from becoming an
// uncontrolled spend — one dispatching comment per scope per interval, and a
// cumulative cap per scope. Both record comment.trigger_throttled and dispatch
// nothing.
func TestMentionThrottled(t *testing.T) {
	t.Run("min interval", func(t *testing.T) {
		root := t.TempDir()
		s := newCommentService(t, root, func(cfg *config.Config) {
			cfg.Server.CommentTrigger = config.CommentTriggerConfig{
				MinIntervalSec: commentIntPtr(60), MaxPerScope: commentIntPtr(10),
			}
		})
		src := commentSourceJob(t, s)

		_, first, err := s.Comment(jobstore.CommentScopeJob, src.ID, "alice", jobstore.CommentAuthorUser, "@ok 第一件")
		if err != nil {
			t.Fatalf("Comment: %v", err)
		}
		if len(first) != 1 {
			t.Fatalf("first comment dispatched %+v, want one", first)
		}
		_, second, err := s.Comment(jobstore.CommentScopeJob, src.ID, "alice", jobstore.CommentAuthorUser, "@ok 第二件")
		if err != nil {
			t.Fatalf("Comment: %v", err)
		}
		if len(second) != 0 {
			t.Fatalf("the second comment inside the interval dispatched %+v, want none", second)
		}
		detail := commentEventDetail(t, s, src.ID, EventCommentTriggerThrottled)
		if !strings.Contains(detail, "min_interval") {
			t.Fatalf("throttle detail = %q, want reason min_interval", detail)
		}
	})

	t.Run("cumulative cap", func(t *testing.T) {
		root := t.TempDir()
		s := newCommentService(t, root, func(cfg *config.Config) {
			cfg.Server.CommentTrigger = config.CommentTriggerConfig{
				MinIntervalSec: commentIntPtr(0), MaxPerScope: commentIntPtr(1),
			}
		})
		src := commentSourceJob(t, s)

		if _, first, err := s.Comment(jobstore.CommentScopeJob, src.ID, "alice", jobstore.CommentAuthorUser, "@ok 一件"); err != nil || len(first) != 1 {
			t.Fatalf("first comment: dispatched=%+v err=%v", first, err)
		}
		if _, second, err := s.Comment(jobstore.CommentScopeJob, src.ID, "alice", jobstore.CommentAuthorUser, "@ok 又一件"); err != nil || len(second) != 0 {
			t.Fatalf("over-cap comment: dispatched=%+v err=%v, want none", second, err)
		}
		detail := commentEventDetail(t, s, src.ID, EventCommentTriggerThrottled)
		if !strings.Contains(detail, "max_per_scope") {
			t.Fatalf("throttle detail = %q, want reason max_per_scope", detail)
		}
		// The throttle is PER scope: a different job's thread still dispatches.
		other := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "ok", Runner: "local", Cwd: ".",
			Title: "另一个", Prompt: "x", TimeoutSec: 30,
		})
		if _, d, err := s.Comment(jobstore.CommentScopeJob, other.ID, "alice", jobstore.CommentAuthorUser, "@ok 另一条线"); err != nil || len(d) != 1 {
			t.Fatalf("another scope: dispatched=%+v err=%v, want one", d, err)
		}
	})

	t.Run("per comment interval", func(t *testing.T) {
		root := t.TempDir()
		s := newCommentService(t, root, func(cfg *config.Config) {
			cfg.Server.CommentTrigger = config.CommentTriggerConfig{
				MinIntervalSec: commentIntPtr(60), MaxPerScope: commentIntPtr(10),
			}
		})
		src := commentSourceJob(t, s)
		// Two mentions in ONE comment are one dispatching comment: the interval gate is
		// evaluated per comment, not per mention.
		_, d, err := s.Comment(jobstore.CommentScopeJob, src.ID, "alice", jobstore.CommentAuthorUser, "@ok @reviewer 分头做")
		if err != nil {
			t.Fatalf("Comment: %v", err)
		}
		if len(d) != 2 {
			t.Fatalf("dispatched = %+v, want two (the interval applies to the comment)", d)
		}
	})
}

func commentIntPtr(v int) *int { return &v }

// TestMentionUnknownOrDisallowedAgentExplains: a mention that names nothing, or names
// an agent this project does not allow, is KEPT in the thread and answered with a
// system comment saying why — never a silent no-op and never a dispatch.
func TestMentionUnknownOrDisallowedAgentExplains(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		root := t.TempDir()
		s := newCommentService(t, root, nil)
		src := commentSourceJob(t, s)

		cm, dispatched, err := s.Comment(jobstore.CommentScopeJob, src.ID, "alice", jobstore.CommentAuthorUser, "@nobody 干活")
		if err != nil {
			t.Fatalf("Comment: %v", err)
		}
		if len(dispatched) != 0 {
			t.Fatalf("dispatched = %+v, want none", dispatched)
		}
		rows, err := s.ListComments(jobstore.CommentScopeJob, src.ID)
		if err != nil {
			t.Fatalf("ListComments: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("thread = %+v, want the comment + one system explanation", rows)
		}
		explanation, ok := systemCommentOf(rows)
		if !ok {
			t.Fatalf("thread = %+v, want a system explanation", rows)
		}
		if !strings.Contains(explanation.Body, "nobody") {
			t.Fatalf("explanation = %q, want it to name the mention", explanation.Body)
		}
		if rows[0].ID != cm.ID && rows[1].ID != cm.ID {
			t.Fatalf("the comment itself is missing from the thread: %+v", rows)
		}
		if cm.TriggeredJobID != "" {
			t.Fatalf("comment row = %+v, want it unlinked", cm)
		}
	})

	t.Run("not allowed in project", func(t *testing.T) {
		root := t.TempDir()
		s := newCommentService(t, root, nil)
		src := submitAndWait(t, s, JobRequest{
			ProjectKey: "locked", Agent: agent.ExecAgentKey, Runner: "local", Cwd: ".",
			Cmd: testcmdEchoArgs(), Title: "locked job", TimeoutSec: 30,
		})

		_, dispatched, err := s.Comment(jobstore.CommentScopeJob, src.ID, "alice", jobstore.CommentAuthorUser, "@ok 你来")
		if err != nil {
			t.Fatalf("Comment: %v", err)
		}
		if len(dispatched) != 0 {
			t.Fatalf("dispatched = %+v, want none (ok is not in locked's allowlist)", dispatched)
		}
		rows, err := s.ListComments(jobstore.CommentScopeJob, src.ID)
		if err != nil {
			t.Fatalf("ListComments: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("thread = %+v, want the comment + a system explanation", rows)
		}
		explanation, ok := systemCommentOf(rows)
		if !ok {
			t.Fatalf("thread = %+v, want a system explanation", rows)
		}
		if !strings.Contains(explanation.Body, "allow") {
			t.Fatalf("explanation = %q, want it to name the allowlist", explanation.Body)
		}
	})

	t.Run("explained even while throttled", func(t *testing.T) {
		// The rate gate closes the DISPATCH, not the explanation: a mention that could
		// never dispatch still says why in the thread, because "nothing happened" is
		// what the person needs the reason for most.
		root := t.TempDir()
		s := newCommentService(t, root, func(cfg *config.Config) {
			cfg.Server.CommentTrigger = config.CommentTriggerConfig{
				MinIntervalSec: commentIntPtr(60), MaxPerScope: commentIntPtr(10),
			}
		})
		src := commentSourceJob(t, s)

		if _, d, err := s.Comment(jobstore.CommentScopeJob, src.ID, "alice", jobstore.CommentAuthorUser, "@ok 一件"); err != nil || len(d) != 1 {
			t.Fatalf("first comment: dispatched=%+v err=%v", d, err)
		}
		_, d, err := s.Comment(jobstore.CommentScopeJob, src.ID, "alice", jobstore.CommentAuthorUser, "@nobody 另一件")
		if err != nil {
			t.Fatalf("Comment: %v", err)
		}
		if len(d) != 0 {
			t.Fatalf("dispatched = %+v, want none", d)
		}
		rows, err := s.ListComments(jobstore.CommentScopeJob, src.ID)
		if err != nil {
			t.Fatalf("ListComments: %v", err)
		}
		explanation, ok := systemCommentOf(rows)
		if !ok {
			t.Fatalf("thread = %+v, want a system explanation despite the throttle", rows)
		}
		if !strings.Contains(explanation.Body, "nobody") {
			t.Fatalf("explanation = %q", explanation.Body)
		}
	})
}

// systemCommentOf returns the system explanation in a thread (order-independent: two
// comments written in the same second are ordered by id, not by who wrote them).
func systemCommentOf(rows []jobstore.Comment) (jobstore.Comment, bool) {
	for _, r := range rows {
		if r.AuthorKind == jobstore.CommentAuthorSystem {
			return r, true
		}
	}
	return jobstore.Comment{}, false
}

// testcmdEchoArgs is a tiny cross-platform argv for the exec source jobs in these
// tests.
func testcmdEchoArgs() []string { return []string{"go", "version"} }

// TestMultipleMentionsDispatchEach: one comment may hand work to several agents; each
// mention starts its own job, and both count toward the scope's trigger budget.
func TestMultipleMentionsDispatchEach(t *testing.T) {
	root := t.TempDir()
	s := newCommentService(t, root, func(cfg *config.Config) {
		// The two dispatched jobs share the source cwd; the JOB-11 dir lock would park
		// one of them behind the other, which this test does not need.
		off := false
		cfg.Server.DirLock = &off
	})
	src := commentSourceJob(t, s)

	cm, dispatched, err := s.Comment(jobstore.CommentScopeJob, src.ID, "alice", jobstore.CommentAuthorUser, "@ok 和 @reviewer 分头做")
	if err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if len(dispatched) != 2 {
		t.Fatalf("dispatched = %+v, want two jobs", dispatched)
	}
	if dispatched[0].JobID == dispatched[1].JobID {
		t.Fatalf("both mentions got the same job: %+v", dispatched)
	}
	seen := map[string]string{}
	for _, d := range dispatched {
		seen[d.JobID] = d.Mention
	}
	if seen[dispatched[0].JobID] == "" {
		t.Fatalf("dispatch ids missing: %+v", dispatched)
	}
	// The row links the first job (one column, one link) and the thread reports both.
	row, ok, err := s.Meta().GetComment(cm.ID)
	if err != nil || !ok {
		t.Fatalf("GetComment: ok=%v err=%v", ok, err)
	}
	if row.TriggeredJobID != dispatched[0].JobID {
		t.Fatalf("triggered_job_id = %q, want the first dispatch %q", row.TriggeredJobID, dispatched[0].JobID)
	}
	if row.MentionsJSON != `["ok","reviewer"]` {
		t.Fatalf("mentions_json = %q", row.MentionsJSON)
	}
	n, _, err := s.Meta().CommentTriggerStats(jobstore.CommentScopeJob, src.ID)
	if err != nil {
		t.Fatalf("CommentTriggerStats: %v", err)
	}
	if n != 1 {
		t.Fatalf("trigger stats = %d, want 1 dispatching comment", n)
	}
}
