package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
)

// Comment event types (MCP-05 阶段 A, design §二.A.5). None of them is in the default
// notification set (notify.DefaultTriggerEvents): a comment is conversation, not a "a
// human must act" signal, so a subscriber lists them explicitly.
//
//   - comment.created            {comment_id,scope,scope_id,author,author_kind,mentions}
//     one row per comment, whatever its author and whatever it triggers.
//   - comment.triggered          {comment_id,job_id,mention,kind}
//     one row per job an @-mention started — the receipt that a comment spent money.
//   - comment.trigger_throttled  {comment_id,reason,scope,scope_id,count,last_at}
//     the throttle refused the whole comment (reason: min_interval|max_per_scope).
//   - comment.mention_rejected   {comment_id,mention,reason,error?}
//     one mention started nothing (reason: unknown|not_allowed|submit_failed); the
//     thread also carries a system comment saying so.
const (
	EventCommentCreated          = "comment.created"
	EventCommentTriggered        = "comment.triggered"
	EventCommentTriggerThrottled = "comment.trigger_throttled"
	EventCommentMentionRejected  = "comment.mention_rejected"
)

// commentReportTailLines / commentReportTailBytes bound the "最近一次汇报摘要" a
// dispatched job receives: the commented job's stdout tail, in lines, read through a
// byte cap so a chatty job cannot paste a megabyte into another job's prompt.
const (
	commentReportTailLines = 40
	commentReportTailBytes = 64 << 10
)

// CommentDispatch is one job a comment's @-mention started (MCP-05 阶段 A). Kind is
// "agent" (the mention named an agent) or "role" (it named a role, whose agent the
// submit resolved) — the caller-facing receipt the CLI, the web thread and MCP all
// render as "→ 已派发 job <id>".
type CommentDispatch struct {
	Mention string `json:"mention"`
	Kind    string `json:"kind"`
	JobID   string `json:"job_id"`
}

// Comment errors. Both are sentinels so the entry layers map them without string
// matching: an unknown target is a 404, a malformed comment a 400.
var (
	// ErrUnknownCommentTarget means the scope/scope_id names no job, plan or todo.
	ErrUnknownCommentTarget = errors.New("unknown comment target")
	// ErrInvalidComment means the scope, body or author is unusable.
	ErrInvalidComment = errors.New("invalid comment")
)

// ParseMentions returns the @-names a comment body mentions, in first-seen order and
// deduplicated.
//
// The grammar is deliberately conservative, because a false positive here starts a job:
//   - a mention is `@` followed by [A-Za-z0-9_-]+ and NOT preceded by a word character
//     (so `email@x.com` and `foo-@bar` are not mentions, `(@omp)` and `@omp.` are);
//   - anything inside a fenced code block (```) or an inline code span (`…`) is code
//     being DISCUSSED, not an instruction, and is skipped.
func ParseMentions(body string) []string {
	if body == "" {
		return nil
	}
	scan := blankCodeRegions(body)
	var out []string
	seen := make(map[string]bool)
	for i := 0; i < len(scan); i++ {
		if scan[i] != '@' {
			continue
		}
		if i > 0 && isMentionNameByte(scan[i-1]) {
			continue
		}
		j := i + 1
		for j < len(scan) && isMentionNameByte(scan[j]) {
			j++
		}
		if j == i+1 {
			continue
		}
		name := scan[i+1 : j]
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
		i = j - 1
	}
	return out
}

// isMentionNameByte reports whether b may appear in a mention name. The same set is
// used to reject a mention whose `@` is glued to a word (an e-mail address).
func isMentionNameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_' || b == '-':
		return true
	}
	return false
}

// blankCodeRegions replaces every fenced code block and inline code span in s with
// spaces of the same length, so the mention scanner can run over the result without
// tracking offsets. Fences are blanked first (which also hides the backticks inside
// them from the inline pass); an unterminated fence swallows the rest of the body.
func blankCodeRegions(s string) string {
	out := []byte(s)
	for i := 0; ; {
		start := strings.Index(s[i:], "```")
		if start < 0 {
			break
		}
		start += i
		end := len(s)
		if close := strings.Index(s[start+3:], "```"); close >= 0 {
			end = start + 3 + close + 3
		}
		blankBytes(out, start, end)
		i = end
	}
	// Inline spans: a `pair` within one line. A lone backtick is not a span.
	for i := 0; i < len(out); i++ {
		if out[i] != '`' {
			continue
		}
		close := -1
		for j := i + 1; j < len(out); j++ {
			if out[j] == '\n' {
				break
			}
			if out[j] == '`' {
				close = j
				break
			}
		}
		if close < 0 {
			continue
		}
		blankBytes(out, i, close+1)
		i = close
	}
	return string(out)
}

// blankBytes overwrites [from,to) with spaces, preserving newlines so line structure
// (and therefore the inline-span scan) still reads correctly.
func blankBytes(b []byte, from, to int) {
	if to > len(b) {
		to = len(b)
	}
	for i := from; i < to; i++ {
		if b[i] != '\n' {
			b[i] = ' '
		}
	}
}

// commentTarget is the resolved object a comment is written on: everything a dispatch
// needs from it (MCP-05 阶段 A, design §二.A.3) — the project the new job runs in, the
// cwd it inherits, the plan/todo it is bound to, the event scope its events land on,
// and the context block prepended to the new job's prompt.
type commentTarget struct {
	scope      string
	scopeID    string
	eventScope string
	projectKey string
	cwd        string
	// runner is the runner a dispatch inherits: the commented JOB's own runner (the
	// work continues where it ran), or a todo's configured dispatch runner. Empty
	// means the built-in local runner — a plan carries no runner of its own, so a
	// plan-level mention runs locally unless the item it is really about says
	// otherwise.
	runner  string
	planID  string
	todoID  string
	context string
}

// Comment records a comment on a job, plan or plan-todo and, when a HUMAN wrote it,
// dispatches one job per @-mention through the SAME Submit entry every other caller
// uses (MCP-05 阶段 A, design §二.A.2/A.3).
//
// authorKind is decided by the entry layer (callerKind for HTTP, the calling job for
// MCP), never by the body. The gates, in order:
//
//	author kind   only "user" triggers (agent comments are recorded; the leader
//	              whitelist of 阶段 B extends this — see commentAuthorMayTrigger)
//	existence     the mention must name a configured agent or a configured role
//	allowlist     that agent must be allowed in the target's project
//	throttle      one dispatching comment per scope per interval, and a per-scope
//	              lifetime cap (server.comment_trigger)
//
// A refused mention is never silent: the comment is kept and a `system` comment says
// why. The returned []CommentDispatch is empty when nothing was started.
func (s *Service) Comment(scope, scopeID, author, authorKind, body string) (jobstore.Comment, []CommentDispatch, error) {
	if !jobstore.ValidCommentScope(scope) {
		return jobstore.Comment{}, nil, fmt.Errorf("%w: unknown scope %q", ErrInvalidComment, scope)
	}
	if strings.TrimSpace(body) == "" {
		return jobstore.Comment{}, nil, fmt.Errorf("%w: empty body", ErrInvalidComment)
	}
	if author == "" || !jobstore.ValidCommentAuthorKind(authorKind) {
		return jobstore.Comment{}, nil, fmt.Errorf("%w: a comment needs an author and author kind", ErrInvalidComment)
	}
	cfg := s.config()
	target, err := s.commentTarget(scope, scopeID)
	if err != nil {
		return jobstore.Comment{}, nil, err
	}

	mentions := ParseMentions(body)
	cm := jobstore.Comment{
		ID: "cm-" + RandomSuffix(), Scope: scope, ScopeID: scopeID,
		Author: author, AuthorKind: authorKind, Body: body,
		MentionsJSON: mentionsJSON(mentions), CreatedAt: s.nowFn().Unix(),
	}
	if err := s.meta.InsertComment(cm); err != nil {
		return jobstore.Comment{}, nil, err
	}
	s.recordEvent(target.eventScope, EventCommentCreated, map[string]any{
		"comment_id": cm.ID, "scope": scope, "scope_id": scopeID,
		"author": author, "author_kind": authorKind, "mentions": mentions,
	})
	if !commentAuthorMayTrigger(authorKind) || len(mentions) == 0 {
		return cm, nil, nil
	}
	dispatched := s.dispatchCommentMentions(cfg, target, &cm, mentions)
	return cm, dispatched, nil
}

// ListComments returns one object's thread, oldest first. An unknown target is
// ErrUnknownCommentTarget (a 404, not an empty thread).
func (s *Service) ListComments(scope, scopeID string) ([]jobstore.Comment, error) {
	if _, err := s.commentTarget(scope, scopeID); err != nil {
		return nil, err
	}
	return s.meta.ListComments(scope, scopeID)
}

// commentAuthorMayTrigger is the author gate of 阶段 A: only a person's comment starts
// work — an agent's comment (an MCP tool call from inside a job) is recorded and
// nothing else. 阶段 B extends this seam with the supervisor.leader whitelist (design
// §二.B); nothing else in this file needs to change for that.
func commentAuthorMayTrigger(authorKind string) bool {
	return authorKind == jobstore.CommentAuthorUser
}

// commentTarget resolves the commented object, refusing an unknown id.
func (s *Service) commentTarget(scope, scopeID string) (commentTarget, error) {
	switch scope {
	case jobstore.CommentScopeJob:
		res, ok := s.Get(scopeID)
		if !ok {
			return commentTarget{}, fmt.Errorf("%w: no job %q", ErrUnknownCommentTarget, scopeID)
		}
		return commentTarget{
			scope: scope, scopeID: scopeID, eventScope: res.ID,
			projectKey: res.ProjectKey, cwd: commentJobCwd(res), runner: res.Runner,
			context: s.commentJobContext(res),
		}, nil
	case jobstore.CommentScopePlan:
		p, ok, err := s.meta.GetPlan(scopeID)
		if err != nil {
			return commentTarget{}, err
		}
		if !ok {
			return commentTarget{}, fmt.Errorf("%w: no plan %q", ErrUnknownCommentTarget, scopeID)
		}
		return commentTarget{
			scope: scope, scopeID: scopeID, eventScope: PlanEventScope(p.PlanID),
			projectKey: p.ProjectKey, planID: p.PlanID,
			context: s.commentPlanContext(p),
		}, nil
	case jobstore.CommentScopeTodo:
		t, ok, err := s.meta.GetTodo(scopeID)
		if err != nil {
			return commentTarget{}, err
		}
		if !ok {
			return commentTarget{}, fmt.Errorf("%w: no todo %q", ErrUnknownCommentTarget, scopeID)
		}
		projectKey := t.ProjectKey
		ctx := s.commentTodoContext(t)
		if p, ok, err := s.meta.GetPlan(t.PlanID); err == nil && ok {
			if projectKey == "" {
				// A todo inherits the plan's project (PLAN-02 P2), exactly like the
				// todo dispatcher: the item's own project wins when it has one.
				projectKey = p.ProjectKey
			}
		}
		return commentTarget{
			scope: scope, scopeID: scopeID, eventScope: PlanEventScope(t.PlanID),
			projectKey: projectKey, cwd: t.Cwd, runner: t.Runner,
			planID: t.PlanID, todoID: t.TodoID,
			context: ctx,
		}, nil
	}
	return commentTarget{}, fmt.Errorf("%w: unknown scope %q", ErrInvalidComment, scope)
}

// dispatchCommentMentions runs the dispatch gates and starts one job per surviving
// mention. It returns the dispatches it started (empty when the throttle refused the
// whole comment).
func (s *Service) dispatchCommentMentions(cfg *config.Config, target commentTarget, cm *jobstore.Comment, mentions []string) []CommentDispatch {
	// Resolve and vet every mention FIRST: a mention that could never dispatch is
	// explained in the thread regardless of the throttle, because "nothing happened" is
	// exactly what the person needs the reason for. The throttle then decides whether
	// the survivors may actually spend anything.
	var ready []commentMention
	for _, name := range mentions {
		kind, agentKey, ok := resolveCommentMention(cfg, name)
		if !ok {
			s.rejectCommentMention(target, cm, name, "unknown",
				fmt.Sprintf("no agent or role named %q is configured", name))
			continue
		}
		if err := commentMentionAllowed(cfg, target.projectKey, agentKey); err != nil {
			s.rejectCommentMention(target, cm, name, "not_allowed", err.Error())
			continue
		}
		ready = append(ready, commentMention{name: name, kind: kind})
	}
	if len(ready) == 0 {
		return nil
	}

	minIntervalSec, maxPerScope := cfg.EffectiveCommentTrigger()
	if reason, count, lastAt := s.commentThrottleReason(cm, minIntervalSec, maxPerScope); reason != "" {
		s.recordEvent(target.eventScope, EventCommentTriggerThrottled, map[string]any{
			"comment_id": cm.ID, "reason": reason, "scope": cm.Scope, "scope_id": cm.ScopeID,
			"count": count, "last_at": lastAt,
		})
		return nil
	}

	var out []CommentDispatch
	for _, m := range ready {
		req := target.dispatchRequest(cm, m.name, m.kind)
		res, err := s.Submit(req)
		if err != nil {
			s.rejectCommentMention(target, cm, m.name, "submit_failed", err.Error())
			continue
		}
		if len(out) == 0 {
			// One link column, one link: the FIRST job this comment started is the one
			// the row points at (the full set is returned to the caller and recorded as
			// comment.triggered events).
			if _, err := s.meta.SetCommentTriggeredJob(cm.ID, res.ID); err == nil {
				cm.TriggeredJobID = res.ID
			}
		}
		s.recordEvent(target.eventScope, EventCommentTriggered, map[string]any{
			"comment_id": cm.ID, "job_id": res.ID, "mention": m.name, "kind": m.kind,
		})
		out = append(out, CommentDispatch{Mention: m.name, Kind: m.kind, JobID: res.ID})
	}
	return out
}

// commentMention is one @-name that survived resolution and the allowlist gate.
type commentMention struct {
	name string
	kind string
}

// commentThrottleReason decides whether this comment may dispatch at all, and says
// which gate refused it ("" = allowed) along with the scope's current stats, so the
// event can carry them. The interval gate is evaluated ONCE per comment, so several
// mentions in one comment are one dispatching comment; the lifetime cap is the count of
// dispatching comments already recorded on the scope.
func (s *Service) commentThrottleReason(cm *jobstore.Comment, minIntervalSec, maxPerScope int) (reason string, count int, lastAt int64) {
	if minIntervalSec <= 0 && maxPerScope <= 0 {
		return "", 0, 0
	}
	count, lastAt, err := s.meta.CommentTriggerStats(cm.Scope, cm.ScopeID)
	if err != nil {
		return "", 0, 0
	}
	if maxPerScope > 0 && count >= maxPerScope {
		return "max_per_scope", count, lastAt
	}
	if minIntervalSec > 0 && lastAt > 0 && cm.CreatedAt-lastAt < int64(minIntervalSec) {
		return "min_interval", count, lastAt
	}
	return "", count, lastAt
}

// rejectCommentMention records why one mention started nothing: an event on the scope
// plus a `system` comment in the thread, so the person who typed the mention reads the
// reason where they wrote it (MCP-05 阶段 A: never a silent no-op).
func (s *Service) rejectCommentMention(target commentTarget, cm *jobstore.Comment, mention, reason, detail string) {
	s.recordEvent(target.eventScope, EventCommentMentionRejected, map[string]any{
		"comment_id": cm.ID, "mention": mention, "reason": reason, "error": detail,
	})
	s.insertSystemComment(target.scope, target.scopeID, fmt.Sprintf("@%s 没有派发：%s", mention, detail))
}

// insertSystemComment appends gofer's own explanation to a thread. It goes through the
// same store insert as every other comment (and records comment.created), so the thread
// has ONE shape and the UI needs no special case.
func (s *Service) insertSystemComment(scope, scopeID, body string) {
	cm := jobstore.Comment{
		ID: "cm-" + RandomSuffix(), Scope: scope, ScopeID: scopeID,
		Author: commentSystemAuthor, AuthorKind: jobstore.CommentAuthorSystem,
		Body: body, CreatedAt: s.nowFn().Unix(),
	}
	if err := s.meta.InsertComment(cm); err != nil {
		return
	}
	s.recordEvent(s.commentEventScope(scope, scopeID), EventCommentCreated, map[string]any{
		"comment_id": cm.ID, "scope": scope, "scope_id": scopeID,
		"author": commentSystemAuthor, "author_kind": jobstore.CommentAuthorSystem,
	})
}

// commentSystemAuthor is the author stamped on gofer's own explanations.
const commentSystemAuthor = "gofer"

// commentEventScope maps a comment's scope onto the id its events are recorded on: a
// job's own timeline for a job, the plan's scope for a plan or one of its todos.
func (s *Service) commentEventScope(scope, scopeID string) string {
	switch scope {
	case jobstore.CommentScopePlan:
		return PlanEventScope(scopeID)
	case jobstore.CommentScopeTodo:
		if t, ok, err := s.meta.GetTodo(scopeID); err == nil && ok {
			return PlanEventScope(t.PlanID)
		}
	}
	return scopeID
}

// commentJobCwd returns the RELATIVE cwd the commented job's request carried — the
// only cwd a dispatch can inherit. The resolved cwd on the job row is absolute and
// machine-specific, and validate refuses an absolute one ("cwd must be relative"), so
// reading it back from request_json is what keeps a handover in the same tree without
// pinning it to one machine's path. An unparsable request contributes no cwd, which
// means "the project's default".
func commentJobCwd(res JobResult) string {
	if res.RequestJSON == "" {
		return ""
	}
	var req JobRequest
	if err := json.Unmarshal([]byte(res.RequestJSON), &req); err != nil {
		return ""
	}
	return req.Cwd
}

// mentionsJSON encodes the parsed mention list for the row. No mentions is "" (NULL in
// the column), which is what "this comment mentioned nobody" means.
func mentionsJSON(mentions []string) string {
	if len(mentions) == 0 {
		return ""
	}
	b, err := json.Marshal(mentions)
	if err != nil {
		return ""
	}
	return string(b)
}

// resolveCommentMention resolves an @-name to what it dispatches: an agent key (the
// mention named an agent) or a role (whose agent the submit resolves). Declare-wins:
// a name that is BOTH a configured agent and a role dispatches to the agent.
func resolveCommentMention(cfg *config.Config, name string) (kind, agentKey string, ok bool) {
	if _, found := agent.ResolveAgent(cfg, name); found {
		return "agent", name, true
	}
	if cfg != nil {
		if r, found := cfg.Roles[name]; found && r.Agent != "" {
			return "role", r.Agent, true
		}
	}
	return "", "", false
}

// commentMentionAllowed applies the project's allowed_agents gate to a mention. It
// mirrors job.validate exactly: an EMPTY allowed_agents list allows every configured
// agent (config-optimize §13), and only a declared list is enforced — so a mention can
// never be admitted where the same agent would be refused as a job's own agent.
func commentMentionAllowed(cfg *config.Config, projectKey, agentKey string) error {
	allowed, ok := cfg.ProjectAllowedAgents(projectKey)
	if !ok {
		return fmt.Errorf("unknown project %q", projectKey)
	}
	if len(allowed) == 0 {
		return nil
	}
	return agent.CheckAllowed(cfg, projectKey, agentKey)
}

// dispatchRequest builds the job a mention starts: the same Submit entry every other
// caller uses, with the commented object's project/cwd, the mention as agent or role,
// the todo binding when there is one, and tags that point back at the comment.
func (t commentTarget) dispatchRequest(cm *jobstore.Comment, mention, kind string) JobRequest {
	runner := t.runner
	if runner == "" {
		runner = config.BuiltinLocalRunner
	}
	req := JobRequest{
		ProjectKey: t.projectKey,
		Runner:     runner,
		Prompt:     commentDispatchPrompt(t.context, cm.Body),
		Cwd:        t.cwd,
		PlanID:     t.planID,
		TodoID:     t.todoID,
		Tags:       []string{commentTagFromComment, commentTagOf + cm.ID},
		CallerID:   cm.Author,
		Channel:    commentDispatchChannel,
	}
	if kind == "role" {
		req.Role = mention
	} else {
		req.Agent = mention
	}
	return req
}

// The tags a comment-dispatched job carries: where it came from, and which comment.
const (
	commentTagFromComment = "from_comment"
	commentTagOf          = "comment_of:"
	// commentDispatchChannel is the JobRequest.Channel provenance of a comment
	// dispatch ("cli"/"web"/"mcp"/"im" are the interactive ones).
	commentDispatchChannel = "comment"
)

// commentDispatchPrompt renders the prompt a mention hands over: the commented
// object's context, then the comment body verbatim (the human's own words are the
// instruction — never rewritten).
func commentDispatchPrompt(context, body string) string {
	var b strings.Builder
	b.WriteString("你在一条评论里被 @ 点名接手工作（gofer 评论派活）。\n\n")
	if context != "" {
		b.WriteString(context)
	}
	b.WriteString("\n评论正文：\n")
	b.WriteString(strings.TrimSpace(body))
	b.WriteString("\n")
	return b.String()
}

// commentJobContext summarises the commented job: what it is, how it ended and how its
// own report ended — the three things a handover needs.
func (s *Service) commentJobContext(res JobResult) string {
	var b strings.Builder
	title := strings.TrimSpace(res.Title)
	if title == "" {
		title = "(无标题)"
	}
	fmt.Fprintf(&b, "被评论的 job：%s %q\n", res.ID, title)
	fmt.Fprintf(&b, "状态：%s", res.Status)
	if res.Error != "" {
		fmt.Fprintf(&b, "（error: %s）", res.Error)
	}
	b.WriteString("\n")
	if tail := s.commentReportTail(res); tail != "" {
		fmt.Fprintf(&b, "最近一次汇报（stdout 末尾 %d 行）：\n%s\n", commentReportTailLines, tail)
	}
	return b.String()
}

// commentReportTail reads the commented job's stdout tail from its result dir. A job
// that produced nothing (or whose dir is gone) simply contributes no report block — a
// missing log is not a failed handover.
func (s *Service) commentReportTail(res JobResult) string {
	if res.ResultDir == "" {
		return ""
	}
	b, err := store.NewFileStore(filepath.Dir(res.ResultDir)).ReadLogTail(res.ID, store.StreamStdout, commentReportTailBytes)
	if err != nil {
		return ""
	}
	return tailLines(string(b), commentReportTailLines)
}

// commentPlanContext summarises the commented plan: its goal, its state and the
// checklist as it stands (bounded, so a 200-item plan does not become the prompt).
func (s *Service) commentPlanContext(p jobstore.Plan) string {
	var b strings.Builder
	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = "(无标题)"
	}
	fmt.Fprintf(&b, "被评论的 plan：%s %q\n", p.PlanID, title)
	fmt.Fprintf(&b, "状态：%s", p.Status)
	if p.Paused {
		b.WriteString("（已暂停）")
	}
	if p.BlockedTodo != "" {
		fmt.Fprintf(&b, "（阻塞在 %s）", p.BlockedTodo)
	}
	b.WriteString("\n")
	if d := strings.TrimSpace(p.Description); d != "" {
		fmt.Fprintf(&b, "目标：%s\n", d)
	}
	if todos, err := s.meta.ListTodosByPlan(p.PlanID); err == nil && len(todos) > 0 {
		fmt.Fprintf(&b, "待办（%d 项）：\n", len(todos))
		for i, t := range todos {
			if i == commentPlanContextTodos {
				fmt.Fprintf(&b, "  … 还有 %d 项\n", len(todos)-i)
				break
			}
			fmt.Fprintf(&b, "  - [%s] %s %s\n", t.Status, t.TodoID, strings.TrimSpace(t.Title))
		}
	}
	return b.String()
}

// commentPlanContextTodos bounds how many checklist items a plan's context block lists.
const commentPlanContextTodos = 20

// commentTodoContext summarises the commented checklist item.
func (s *Service) commentTodoContext(t jobstore.PlanTodo) string {
	var b strings.Builder
	title := strings.TrimSpace(t.Title)
	if title == "" {
		title = "(无标题)"
	}
	fmt.Fprintf(&b, "被评论的 todo：%s %q（plan %s）\n", t.TodoID, title, t.PlanID)
	fmt.Fprintf(&b, "状态：%s\n", t.Status)
	if n := strings.TrimSpace(t.Note); n != "" {
		fmt.Fprintf(&b, "备注：%s\n", n)
	}
	return b.String()
}

// tailLines returns the last n lines of s (n <= 0 or a shorter string returns it whole).
func tailLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
