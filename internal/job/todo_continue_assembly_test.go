package job

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// The continuation request (assembleContinuation): built on the ordinary request of the
// item, with only the session fields taken from the predecessor's job, and a prompt that
// is rendered completely by the assembler.

// continueRequests runs the decision for the successor and returns the continuation
// request next to the ordinary one it was built on.
func continueRequests(t *testing.T, s *Service, todoID string) (cont, normal JobRequest) {
	t.Helper()
	todo, ok, err := s.meta.GetTodo(todoID)
	if err != nil || !ok {
		t.Fatalf("GetTodo(%s): ok=%v err=%v", todoID, ok, err)
	}
	plan, _, _ := s.meta.GetPlan(todo.PlanID)
	normal, why := s.normalTodoRequest(plan, todo, "")
	if why != "" {
		t.Fatalf("normalTodoRequest: %s", why)
	}
	cont, reason := s.todoContinuationRequest(plan, todo, normal)
	if reason != "" {
		t.Fatalf("todoContinuationRequest: %s", reason)
	}
	return cont, normal
}

// How each JobRequest field of a continuation is decided. A field that is not listed
// fails TestTodoContinuationFieldClassification: whoever adds one must decide.
const (
	// fieldTodo: the value of the ordinary request of the item, untouched.
	fieldTodo = "todo"
	// fieldPred: the value comes from the predecessor's job (checked by value).
	fieldPred = "pred"
	// fieldCleared: never copied from the predecessor and never set: it is zero even
	// though the predecessor had one.
	fieldCleared = "cleared"
	// fieldAssembled: set by the assembler; the value is pinned in the tests below.
	fieldAssembled = "assembled"
	// fieldInternal: unexported bookkeeping of Submit, zero on a request being built.
	fieldInternal = "internal"
)

var continuationFieldClasses = map[string]string{
	// from the predecessor
	"SessionID": fieldPred, "ResumedFrom": fieldPred, "SourceJobID": fieldPred, "Cwd": fieldPred,
	"InheritWorktree": fieldPred, "Runner": fieldPred, "MCP": fieldPred,
	// set by the assembler
	"Agent": fieldAssembled, "Cmd": fieldAssembled, "Prompt": fieldAssembled, "Model": fieldAssembled,
	"TimeoutSec": fieldAssembled, "KnowledgeCapture": fieldAssembled, "RulesResolved": fieldAssembled,
	"SkillsResolved": fieldAssembled, "ResumeSourceAgent": fieldAssembled, "BaselineSHA": fieldAssembled,
	// not inherited
	"Hold": fieldCleared, "HoldReason": fieldCleared, "HoldTimeoutSec": fieldCleared, "Fallback": fieldCleared,
	"Deliver": fieldCleared, "DeliverDraft": fieldCleared, "DeliverBranch": fieldCleared,
	"LockPaths": fieldCleared, "Secrets": fieldCleared, "EnvFiles": fieldCleared, "ReviewFixed": fieldCleared,
	"Template": fieldCleared, "TemplateVars": fieldCleared, "Worktree": fieldCleared, "WorktreeBase": fieldCleared,
	"WorktreeName": fieldCleared, "WorktreeBranch": fieldCleared,
	"ExclusiveDir": fieldCleared, "Env": fieldCleared,
	// the ordinary request of the item
	"ProjectKey": fieldTodo, "Title": fieldTodo, "Acceptance": fieldTodo, "Scope": fieldTodo, "NoScopeDiscipline": fieldTodo,
	"Budget": fieldTodo, "BudgetFixed": fieldTodo, "Verify": fieldTodo, "VerifyTimeoutSec": fieldTodo, "NoVerify": fieldTodo,
	"Review": fieldTodo, "ReadOnly": fieldTodo, "Tags": fieldTodo, "PrimeKeywords": fieldTodo, "Rules": fieldTodo, "NoRules": fieldTodo,
	"TodoID": fieldTodo, "PlanID": fieldTodo, "TodoForeign": fieldTodo, "Channel": fieldTodo, "CallerID": fieldTodo, "Client": fieldTodo,
	"OriginCaller":       fieldTodo,
	"ExtraCeilingOwners": fieldTodo, "SystemJob": fieldTodo, "InternalExec": fieldTodo,
	"SourceSessionID": fieldTodo, "Notify": fieldTodo, "NotifySession": fieldTodo, "NotifyDegradable": fieldTodo,
	"FallbackAgents": fieldTodo, "AgentArgs": fieldTodo, "FromSession": fieldTodo, "Skills": fieldTodo, "NoSkills": fieldTodo,
	"NoMCP": fieldTodo, "MCPResolved": fieldTodo, "MCPSpecs": fieldTodo, "Uploads": fieldTodo, "Collect": fieldTodo,
	"Session": fieldTodo, "IdleTimeoutSec": fieldTodo, "MaxSessionSec": fieldTodo, "Interactive": fieldTodo,
	"StallTimeoutSec": fieldTodo, "LockWaitSec": fieldTodo, "DirWaitMaxSec": fieldTodo, "Cols": fieldTodo, "Rows": fieldTodo,
	"InitialInput": fieldTodo, "InitialInputQuietMs": fieldTodo, "Stdin": fieldTodo, "RecordPty": fieldTodo,
	"Role": fieldTodo, "SystemPrompt": fieldTodo, "Messenger": fieldTodo, "MessengerMeta": fieldTodo,
	"ResolvedLockPaths": fieldTodo, "LeaderOfPlan": fieldTodo, "Steward": fieldTodo, "JobToken": fieldTodo,
	"CredentialExternal": fieldTodo, "RequestID": fieldTodo, "WorkerID": fieldTodo, "WorkerLabels": fieldTodo,
	"UpdateExempt": fieldTodo, "AutoResumeAttempt": fieldTodo, "StrictTimeout": fieldTodo,
	"EnvDenyExtra": fieldTodo, "Sync": fieldTodo, "WaitTimeoutSec": fieldTodo, "IssueID": fieldTodo, "TrackerID": fieldTodo,
	"Retry": fieldTodo, "WorkflowID": fieldTodo, "StepIndex": fieldTodo, "Attempt": fieldTodo, "FanIndex": fieldTodo,
	"NoFallback": fieldTodo, "FellBackFrom": fieldTodo, "RequestedAgent": fieldTodo,
	// Submit's own bookkeeping
	"admissionPermit": fieldInternal, "heldJobID": fieldInternal, "held": fieldInternal, "decisions": fieldInternal,
	"ruleRefs": fieldInternal, "ruleBytes": fieldInternal, "rulesSkipped": fieldInternal,
}

// TestTodoContinuationFieldClassification enumerates EVERY field of JobRequest and
// requires it to be classified; a new field that nobody decided about fails here. The
// classes are then checked against a real assembly over a predecessor that carries a
// value in every field the continuation must not inherit.
func TestTodoContinuationFieldClassification(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(JobRequest{})
	var unclassified, stale []string
	seen := map[string]bool{}
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		seen[name] = true
		if _, ok := continuationFieldClasses[name]; !ok {
			unclassified = append(unclassified, name)
		}
	}
	for name := range continuationFieldClasses {
		if !seen[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(unclassified) > 0 {
		t.Fatalf("JobRequest fields without a continuation class (add them to continuationFieldClasses): %v", unclassified)
	}
	if len(stale) > 0 {
		t.Fatalf("continuationFieldClasses names fields JobRequest no longer has: %v", stale)
	}

	s, root := newContinueService(t)
	seedContinuePlan(t, s, func(td *jobstore.PlanTodo) {
		td.Note = "succ note"
		td.Acceptance = "criterion one"
		td.Scope = []string{"internal/**"}
		td.Verify = []string{"true"}
		td.Review = true
		td.TimeoutSec = 600
		td.PrimeKeywords = []string{"kw"}
		td.Model = "m-succ"
	})
	// The predecessor holds a value in everything the continuation must not inherit.
	seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, func(j *JobResult) {
		j.WorktreePath = root
		j.RequireReview = true
		j.MCP = []string{"mcp-1"}
		j.RequestJSON = `{"agent":"ca","prompt":"work","cwd":"sub","title":"pred title (resumed)","model":"m-pred","hold":true,"hold_reason":"approve me",` +
			`"hold_timeout_sec":60,"lock_paths":["a"],"secrets":["S1"],"env_files":["/x/.env"],` +
			`"template":"book","vars":{"k":"v"},"worktree":true,"worktree_base":"main","exclusive_dir":false,` +
			`"env":{"E":"1"},"deliver_draft":true,"deliver_branch":"b","fallback":{"candidates":["cb"],"depth":1}}`
	})
	cont, normal := continueRequests(t, s, contSucc)

	wantPred := map[string]any{
		"SessionID": "sess-p", "ResumedFrom": "j-pred", "SourceJobID": "j-pred", "Cwd": "sub",
		"InheritWorktree": true, "Runner": "local", "MCP": []string{"mcp-1"},
	}
	cv, nv := reflect.ValueOf(cont), reflect.ValueOf(normal)
	for i := range typ.NumField() {
		f := typ.Field(i)
		got, base := cv.Field(i), nv.Field(i)
		switch continuationFieldClasses[f.Name] {
		case fieldPred:
			if !reflect.DeepEqual(got.Interface(), wantPred[f.Name]) {
				t.Errorf("%s = %v, want %v from the predecessor", f.Name, got.Interface(), wantPred[f.Name])
			}
		case fieldCleared, fieldInternal:
			if !got.IsZero() {
				t.Errorf("%s = %v, want it empty (never inherited)", f.Name, got)
			}
		case fieldTodo:
			if f.IsExported() && !reflect.DeepEqual(got.Interface(), base.Interface()) {
				t.Errorf("%s = %v, want the ordinary request's %v", f.Name, got.Interface(), base.Interface())
			}
		}
	}
	if cont.Title != "succ item" || strings.Contains(cont.Title, "resumed") {
		t.Errorf("Title = %q, want the item's own title", cont.Title)
	}
	if cont.Model != "m-succ" {
		t.Errorf("Model = %q, want the item's model over the predecessor's m-pred", cont.Model)
	}
	if !cont.RulesResolved || !cont.SkillsResolved {
		t.Errorf("RulesResolved=%v SkillsResolved=%v, want both set", cont.RulesResolved, cont.SkillsResolved)
	}
	if cont.Agent != "exec" || len(cont.Cmd) == 0 || cont.ResumeSourceAgent != "ca" || cont.Prompt != "" {
		t.Errorf("carrier = agent %q cmd %v source %q prompt %q, want the exec carrier of ca", cont.Agent, cont.Cmd, cont.ResumeSourceAgent, cont.Prompt)
	}
	if cont.TimeoutSec != 600 {
		t.Errorf("TimeoutSec = %d, want the item's 600", cont.TimeoutSec)
	}
	if !cont.KnowledgeCapture {
		t.Errorf("KnowledgeCapture = false, want it decided for the item (a plan item job captures)")
	}
}

// TestTodoContinueModelFromItemOrPredecessor: the item's own model wins; without one
// the predecessor's model carries on.
func TestTodoContinueModelFromItemOrPredecessor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, itemModel, want string
	}{
		{"item model overrides", "m-item", "m-item"},
		{"predecessor model inherited", "", "m-pred"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _ := newContinueService(t)
			seedContinuePlan(t, s, func(td *jobstore.PlanTodo) { td.Model = tc.itemModel })
			seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, func(j *JobResult) {
				j.RequestJSON = `{"agent":"ca","prompt":"work","cwd":".","model":"m-pred"}`
			})
			cont, _ := continueRequests(t, s, contSucc)
			if cont.Model != tc.want {
				t.Fatalf("Model = %q, want %q", cont.Model, tc.want)
			}
			if got := strings.Join(cont.Cmd, " "); !strings.Contains(got, "--model "+tc.want) {
				t.Fatalf("argv %q does not carry --model %s", got, tc.want)
			}
		})
	}
}

// TestTodoContinueCLIPrompt: through the cli carrier the whole prompt is the item's
// task book behind the bridging line, followed by its own acceptance, delivery contract
// and time-limit line — and none of what the session already has (rules, the tracker
// prime, the tools section).
func TestTodoContinueCLIPrompt(t *testing.T) {
	s, root := newContinueService(t)
	if err := os.MkdirAll(filepath.Join(root, ".gofer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gofer", "RULES.md"), []byte("RULE-MARK-ZZ keep to the rules\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.config().Tools = []config.Tool{{Name: "tool-mark-zz"}}
	writeTemplate(t, root, "book", "---\nmodel: m-book\n---\nBOOK for {{todo_title}} ({{todo_id}}) var={{who}}\n")
	t.Setenv(config.EnvConfigDir, t.TempDir())
	seedContinuePlan(t, s, func(td *jobstore.PlanTodo) {
		td.Template = "book"
		td.Vars = map[string]string{"who": "me"}
		td.Acceptance = "criterion-mark"
		td.TimeoutSec = 600
	})
	// A twin that is dispatched the ordinary way shows what a fresh session receives.
	if err := s.Meta().InsertPlan(jobstore.Plan{PlanID: "pc2", Title: "Twin plan", ProjectKey: "self", Status: jobstore.PlanOpen, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.Meta().InsertTodo(jobstore.PlanTodo{TodoID: "twin", PlanID: "pc2", Title: "twin item", Assignee: "ca", Template: "book",
		Vars: map[string]string{"who": "me"}, Acceptance: "criterion-mark", TimeoutSec: 600}); err != nil {
		t.Fatal(err)
	}
	seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, nil)

	dt, errT := s.DispatchTodo("twin", "")
	twin := dispatchedJob(t, s, dt, errT)
	// The rules ride the recorded prompt, the tools section only the prompt the agent
	// runs with (its stdout echoes the argv).
	twinSeen := twin.RequestJSON + jobStdout(t, root, twin.ID)
	for _, mark := range []string{"RULE-MARK-ZZ", "tool-mark-zz"} {
		if !strings.Contains(twinSeen, mark) {
			t.Fatalf("control: the ordinary dispatch must carry %q (so its absence below means something)\n%s", mark, twinSeen)
		}
	}

	d, err := s.DispatchTodo(contSucc, "")
	final := dispatchedJob(t, s, d, err)
	if final.Status != StatusDone {
		t.Fatalf("continuation = %s (%s) exit=%v, want done\n%s\n%s", final.Status, final.Error, final.ExitCode, final.RequestJSON, jobStdout(t, root, final.ID))
	}
	if final.ResumedFrom != "j-pred" || final.SessionID != "sess-p" {
		t.Fatalf("lineage = %q / %q", final.ResumedFrom, final.SessionID)
	}
	req := requestOf(t, final)
	all := strings.Join(req.Cmd, "\n") + "\n" + jobStdout(t, root, final.ID)
	want := []string{
		"本任务接着上一项（pred item）在同一个会话里继续。上一项已完成；下面是新的任务说明，以它为准。",
		"BOOK for succ item (" + contSucc + ") var=me",
		"## 验收标准", "criterion-mark",
		"## 交付约定",
		deadlineLinePrefix + " 10 分钟",
	}
	pos := -1
	for _, w := range want {
		i := strings.Index(all, w)
		if i < 0 {
			t.Fatalf("prompt lacks %q:\n%s", w, all)
		}
		if i < pos {
			t.Fatalf("%q is out of order in the prompt:\n%s", w, all)
		}
		pos = i
	}
	for _, bad := range []string{"RULE-MARK-ZZ", "tool-mark-zz", "tracker", "已挂载", "技能"} {
		if strings.Contains(all, bad) {
			t.Fatalf("prompt carries %q, which the session already has:\n%s", bad, all)
		}
	}
	if req.Template != "" || len(req.TemplateVars) != 0 {
		t.Fatalf("request still names the task book: %q %v", req.Template, req.TemplateVars)
	}
	if final.TimeoutSec != 600 {
		t.Fatalf("timeout = %d, want the item's 600 on the carrier", final.TimeoutSec)
	}
}

// TestTodoContinueCLIDefaultTimeout: a carrier is an exec job, which defaults to the
// short exec limit; the continuation of an agent run gets the AGENT limit, and the
// prompt quotes that one.
func TestTodoContinueCLIDefaultTimeout(t *testing.T) {
	t.Parallel()
	s, _ := newContinueService(t)
	seedContinuePlan(t, s, nil)
	seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, nil)
	cont, _ := continueRequests(t, s, contSucc)
	if cont.TimeoutSec != DefaultAgentTimeoutSec {
		t.Fatalf("TimeoutSec = %d, want the agent default %d", cont.TimeoutSec, DefaultAgentTimeoutSec)
	}
}

// TestTodoContinueNoteClearedOnSuccess: a continuation clears the note an earlier
// attempt left.
func TestTodoContinueNoteClearedOnSuccess(t *testing.T) {
	t.Parallel()
	s, _ := newContinueService(t)
	seedContinuePlan(t, s, nil)
	seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, nil)
	if err := s.meta.SetTodoContinueNote(contSucc, continueFallbackNote("old reason")); err != nil {
		t.Fatal(err)
	}
	d, err := s.DispatchTodo(contSucc, "")
	dispatchedJob(t, s, d, err)
	got, _, _ := s.meta.GetTodo(contSucc)
	if got.ContinueNote != "" {
		t.Fatalf("continue_note = %q, want it cleared by a successful continuation", got.ContinueNote)
	}
}

// TestTodoContinueACPOneShot: an ACP continuation loads the predecessor's session,
// runs the item's prompt once and ends — it never parks in awaiting_input.
func TestTodoContinueACPOneShot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{EchoPrompt: true})
	seedContinuePlan(t, s, func(td *jobstore.PlanTodo) {
		td.Assignee = "acpbot"
		td.Acceptance = "acp-criterion"
		td.Note = "acp task note"
	})
	pred := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Prompt: acpTestPrompt, Cwd: ".",
		TimeoutSec: 30, TodoID: contPred, PlanID: contPlan,
	})
	if pred.Status != StatusDone || pred.SessionID == "" {
		t.Fatalf("predecessor = %s / %q", pred.Status, pred.SessionID)
	}
	setTodoStatus(t, s, contPred, jobstore.TodoDone)

	d, err := s.DispatchTodo(contSucc, "")
	if err != nil || d.Job == nil {
		t.Fatalf("dispatch: %+v err=%v", d, err)
	}
	var final JobResult
	wait.For(t, 15*time.Second, "the continuation to end", func() (bool, any) {
		cur, ok := s.Get(d.Job.ID)
		if ok && cur.Status == StatusAwaitingInput {
			t.Errorf("continuation parked in awaiting_input")
			return true, nil
		}
		if ok && IsTerminal(cur.Status) {
			final = cur
			return true, nil
		}
		return false, nil
	})
	if _, ok := s.Wait(d.Job.ID); !ok {
		t.Fatalf("job %s vanished", d.Job.ID)
	}
	final, _ = s.Get(d.Job.ID)
	if final.Status != StatusDone {
		t.Fatalf("continuation = %s (%s), want done", final.Status, final.Error)
	}
	if final.Session || final.ResumedFrom != pred.ID || final.SessionID != pred.SessionID || final.TodoID != contSucc {
		t.Fatalf("continuation = session %v resumed_from %q session_id %q todo %q", final.Session, final.ResumedFrom, final.SessionID, final.TodoID)
	}
	req := requestOf(t, final)
	for _, want := range []string{"本任务接着上一项（pred item）", "acp task note", "## 验收标准", "acp-criterion", "## 交付约定", deadlineLinePrefix} {
		if !strings.Contains(req.Prompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, req.Prompt)
		}
	}
	if req.Agent != "acpbot" {
		t.Fatalf("request agent %q, want the ACP agent itself", req.Agent)
	}
	out := jobStdout(t, root, final.ID)
	if !strings.Contains(out, "ECHO:") && !strings.Contains(final.RequestJSON, "acp-criterion") {
		t.Fatalf("the agent never received the prompt: %q", out)
	}
}
