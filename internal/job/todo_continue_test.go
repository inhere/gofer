package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// The todo-continuation decision (todo_continue.go): ten conditions in order, a claim on
// the predecessor's slot, and a fallback to the ordinary request. Assembling the
// continuation request is built by assembleContinuation (todo_continue_assembly_test.go);
// these tests pin the decision, the claim bookkeeping and the fallback paths.

// contHolds is the expectation of a case in which every condition holds: the decision
// returns a continuation request (no reason) and keeps the predecessor's slot.
const contHolds = "<continuation>"

const (
	contPlan = "pc"
	contPred = "tp"
	contSucc = "ts"
)

// newContinueService builds a service over agents that differ in exactly what the
// conditions look at: ca / cb share a session family and resume (ca takes a model), cm
// resumes but takes no model, cx is another family, cnr cannot resume, cacp is an ACP
// agent that does not load sessions.
func newContinueService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	bin := testcmd.Path(t)
	cli := func(modelArgs []string, family string) config.AgentConfig {
		ac := config.AgentConfig{
			Type: agent.TypeCLIAgent, Command: bin, Args: []string{"argv", "{{prompt}}"},
			SessionFamily: family, ModelArgs: modelArgs,
			// Resuming runs the test binary's own argv echo, so a continuation can finish.
			SessionResume: []string{"argv", "--resume", "{{session_id}}", "{{prompt}}"},
		}
		return ac
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"ca", "cb", "cm", "cx", "cacp", "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"ca": cli([]string{"--model", "{{model}}"}, "fam"),
			"cb": cli(nil, "fam"),
			"cm": cli(nil, "fam"),
			"cx": cli(nil, "other"),
			"cacp": {
				Type: agent.TypeACPAgent, Command: bin, Args: []string{"argv"},
				SessionFamily: "fam", ACP: &config.ACPConfig{LoadSession: boolPtr(false)},
			},
		},
	}
	return newServiceFromCfg(t, root, cfg), root
}

// seedContinuePlan inserts the plan with the done predecessor and its successor; patch
// adjusts the successor before it is stored.
func seedContinuePlan(t *testing.T, s *Service, patch func(*jobstore.PlanTodo)) {
	t.Helper()
	if err := s.Meta().InsertPlan(jobstore.Plan{
		PlanID: contPlan, Title: "Continue plan", ProjectKey: "self", Status: jobstore.PlanOpen,
		CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("insert plan: %v", err)
	}
	if err := s.Meta().InsertTodo(jobstore.PlanTodo{
		TodoID: contPred, PlanID: contPlan, Title: "pred item", Status: jobstore.TodoDone,
		Assignee: "ca", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("insert pred: %v", err)
	}
	succ := jobstore.PlanTodo{
		TodoID: contSucc, PlanID: contPlan, Title: "succ item", Assignee: "ca",
		ContinueFrom: contPred, CreatedAt: 2, UpdatedAt: 2,
	}
	if patch != nil {
		patch(&succ)
	}
	if err := s.Meta().InsertTodo(succ); err != nil {
		t.Fatalf("insert succ: %v", err)
	}
}

// seedContJob stores a finished job of a plan item without running anything.
func seedContJob(t *testing.T, s *Service, id, todoID, session, status string, started int64, edit func(*JobResult)) {
	t.Helper()
	snap := JobResult{
		ID: id, ProjectKey: "self", Agent: "ca", Runner: "local", Status: status,
		TodoID: todoID, PlanID: contPlan, SessionID: session, StartedAt: started, TimeoutSec: 30,
		RequestJSON: `{"agent":"ca","prompt":"work","cwd":"."}`,
	}
	if edit != nil {
		edit(&snap)
	}
	if err := s.persist(snap); err != nil {
		t.Fatalf("seed job %s: %v", id, err)
	}
}

// continueDecision runs the decision for the successor against the stored plan and
// returns its reason ("" = a continuation request came back).
func continueDecision(t *testing.T, s *Service, todoID string) string {
	t.Helper()
	todo, ok, err := s.meta.GetTodo(todoID)
	if err != nil || !ok {
		t.Fatalf("GetTodo(%s): ok=%v err=%v", todoID, ok, err)
	}
	plan, ok, err := s.meta.GetPlan(todo.PlanID)
	if err != nil || !ok {
		t.Fatalf("GetPlan: ok=%v err=%v", ok, err)
	}
	normal, why := s.normalTodoRequest(plan, todo, "")
	if why != "" {
		t.Fatalf("normalTodoRequest: %s", why)
	}
	_, reason := s.todoContinuationRequest(plan, todo, normal)
	return reason
}

func continuedBy(t *testing.T, s *Service, predID string) string {
	t.Helper()
	pred, ok, err := s.meta.GetTodo(predID)
	if err != nil || !ok {
		t.Fatalf("GetTodo(%s): ok=%v err=%v", predID, ok, err)
	}
	return pred.ContinuedBy
}

// TestTodoContinueConditions: every condition has a case that fails it, and a case that
// is just inside it. A todo that passes all ten gets a continuation request and keeps its
// claim.
func TestTodoContinueConditions(t *testing.T) {
	t.Parallel()
	good := func(t *testing.T, s *Service) {
		seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, nil)
	}
	cases := []struct {
		name  string
		patch func(*jobstore.PlanTodo)
		setup func(t *testing.T, s *Service, root string)
		want  string // substring of the reason; contHolds = a continuation request came back
	}{
		{name: "all ten hold", setup: func(t *testing.T, s *Service, _ string) { good(t, s) }, want: contHolds},
		{
			name: "1 predecessor missing",
			setup: func(t *testing.T, s *Service, _ string) {
				good(t, s)
				// A todo cannot be deleted while another continues it: clear the
				// reference's target by pointing at a deleted sibling instead.
				if err := s.Meta().InsertTodo(jobstore.PlanTodo{TodoID: "gone", PlanID: contPlan, Title: "gone"}); err != nil {
					t.Fatal(err)
				}
				setTodoFields(t, s, contSucc, jobstore.TodoPatch{ContinueFrom: ptrStr("gone")})
				if _, err := s.Meta().DeleteTodo("gone"); err != nil {
					t.Fatal(err)
				}
			},
			want: "前序项不存在",
		},
		{
			name: "1 predecessor not done",
			setup: func(t *testing.T, s *Service, _ string) {
				good(t, s)
				setTodoStatus(t, s, contPred, jobstore.TodoDoing)
			},
			want: "未完成",
		},
		{
			name: "2 predecessor has no done job with a session",
			setup: func(t *testing.T, s *Service, _ string) {
				seedContJob(t, s, "j-pred", contPred, "", StatusDone, 100, nil)
			},
			want: "没有已完成且带会话的 job",
		},
		{
			name:  "3 exec assignee",
			patch: func(td *jobstore.PlanTodo) { td.Assignee = agent.ExecAgentKey; td.Cmd = []string{"true"} },
			setup: func(t *testing.T, s *Service, _ string) { good(t, s) },
			want:  "exec 不能续接",
		},
		{
			name:  "3 other session family",
			patch: func(td *jobstore.PlanTodo) { td.Assignee = "cx" },
			setup: func(t *testing.T, s *Service, _ string) { good(t, s) },
			want:  "前序会话不可续接",
		},
		{
			name:  "3 same family other agent",
			patch: func(td *jobstore.PlanTodo) { td.Assignee = "cb" },
			setup: func(t *testing.T, s *Service, _ string) { good(t, s) },
			want:  contHolds,
		},
		{
			name:  "4 another runner",
			patch: func(td *jobstore.PlanTodo) { td.Runner = "w1" },
			setup: func(t *testing.T, s *Service, _ string) { good(t, s) },
			want:  "runner 不同或非本机",
		},
		{
			name: "4 predecessor ran on a worker",
			setup: func(t *testing.T, s *Service, _ string) {
				seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, func(j *JobResult) { j.WorkerID = "w1" })
			},
			want: "runner 不同或非本机",
		},
		{
			name:  "4 the server spelling is the local runner",
			patch: func(td *jobstore.PlanTodo) { td.Runner = "server" },
			setup: func(t *testing.T, s *Service, _ string) { good(t, s) },
			want:  contHolds,
		},
		{
			name:  "5 different cwd",
			patch: func(td *jobstore.PlanTodo) { td.Cwd = "sub" },
			setup: func(t *testing.T, s *Service, _ string) { good(t, s) },
			want:  "cwd",
		},
		{
			name:  "5 same cwd",
			patch: func(td *jobstore.PlanTodo) { td.Cwd = "./" },
			setup: func(t *testing.T, s *Service, _ string) { good(t, s) },
			want:  contHolds,
		},
		{
			name: "6 worktree is gone",
			setup: func(t *testing.T, s *Service, root string) {
				seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, func(j *JobResult) {
					j.WorktreePath = filepath.Join(root, "no-such-worktree")
				})
			},
			want: "worktree 已不存在",
		},
		{
			name: "6 predecessor needs auto delivery",
			setup: func(t *testing.T, s *Service, root string) {
				seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, func(j *JobResult) {
					j.WorktreePath = root
					j.RequestJSON = `{"agent":"ca","prompt":"work","cwd":".","deliver":"merge"}`
				})
			},
			want: "自动交付",
		},
		{
			name: "6 worktree present and no delivery",
			setup: func(t *testing.T, s *Service, root string) {
				seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, func(j *JobResult) { j.WorktreePath = root })
			},
			want: contHolds,
		},
		{
			name: "8 chain limit reached",
			setup: func(t *testing.T, s *Service, _ string) {
				seedContJob(t, s, "j-a", "x-a", "sess-p", StatusDone, 10, nil)
				seedContJob(t, s, "j-b", "x-b", "sess-p", StatusDone, 20, func(j *JobResult) { j.ResumedFrom = "j-a" })
				seedContJob(t, s, "j-c", "x-c", "sess-p", StatusDone, 30, func(j *JobResult) { j.ResumedFrom = "j-b" })
				seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 40, func(j *JobResult) { j.ResumedFrom = "j-c" })
			},
			want: "已连续接续 3 次",
		},
		{
			name: "8 one below the limit, rework does not count",
			setup: func(t *testing.T, s *Service, _ string) {
				seedContJob(t, s, "j-a", "x-a", "sess-p", StatusDone, 10, nil)
				seedContJob(t, s, "j-b", "x-b", "sess-p", StatusDone, 20, func(j *JobResult) { j.ResumedFrom = "j-a" })
				seedContJob(t, s, "j-c", "x-c", "sess-p", StatusDone, 30, func(j *JobResult) { j.ResumedFrom = "j-b" })
				seedContJob(t, s, "j-c2", "x-c", "sess-p", StatusDone, 35, func(j *JobResult) { j.ResumedFrom = "j-c" })
				seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 40, func(j *JobResult) { j.ResumedFrom = "j-c2" })
			},
			want: "已连续接续 3 次",
		},
		{
			name:  "9 acp agent does not load sessions",
			patch: func(td *jobstore.PlanTodo) { td.Assignee = "cacp" },
			setup: func(t *testing.T, s *Service, _ string) { good(t, s) },
			want:  "没有可用的会话续接方式",
		},
		{
			name:  "9 model the agent cannot take",
			patch: func(td *jobstore.PlanTodo) { td.Assignee = "cm"; td.Model = "m-1" },
			setup: func(t *testing.T, s *Service, _ string) { good(t, s) },
			want:  "配置不支持续接",
		},
		{
			name:  "9 model the agent takes",
			patch: func(td *jobstore.PlanTodo) { td.Model = "m-1" },
			setup: func(t *testing.T, s *Service, _ string) { good(t, s) },
			want:  contHolds,
		},
		{
			name: "10 read-only differs",
			setup: func(t *testing.T, s *Service, _ string) {
				seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, func(j *JobResult) { j.ReadOnly = true })
			},
			want: "只读设置不一致",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, root := newContinueService(t)
			seedContinuePlan(t, s, tc.patch)
			tc.setup(t, s, root)
			got := continueDecision(t, s, contSucc)
			if tc.want == contHolds {
				if got != "" {
					t.Fatalf("reason = %q, want a continuation request", got)
				}
				// A todo that passes everything keeps the slot it claimed.
				if holder := continuedBy(t, s, contPred); holder != contSucc {
					t.Fatalf("continued_by = %q after a continuation, want %q", holder, contSucc)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("reason = %q, want it to contain %q", got, tc.want)
			}
			// A failed condition leaves the predecessor's slot free.
			if holder := continuedBy(t, s, contPred); holder != "" {
				t.Fatalf("continued_by = %q after the decision, want the slot free", holder)
			}
		})
	}
}

// TestTodoContinueReadOnlyFromTaskBook: condition 10 looks at the request as Submit
// will see it, i.e. with the task book's read_only default folded in.
func TestTodoContinueReadOnlyFromTaskBook(t *testing.T) {
	s, root := newContinueService(t)
	t.Setenv(config.EnvConfigDir, t.TempDir())
	writeTemplate(t, root, "ro-book", "---\nread_only: true\n---\nlook at {{todo_title}}\n")
	seedContinuePlan(t, s, func(td *jobstore.PlanTodo) { td.Template = "ro-book" })
	seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, nil)
	if got := continueDecision(t, s, contSucc); !strings.Contains(got, "只读设置不一致") {
		t.Fatalf("reason = %q, want the read-only mismatch (task book is read-only, predecessor is not)", got)
	}
	seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, func(j *JobResult) { j.ReadOnly = true })
	if got := continueDecision(t, s, contSucc); got != "" {
		t.Fatalf("reason = %q, want the conditions to pass once the predecessor is read-only too", got)
	}
}

// TestTodoContinuePredecessorJobSelection: the session continued is the one of the MOST
// RECENT job of the predecessor that ended done with a session, however many newer
// failed / session-less attempts sit in front of it (a fixed page would miss it).
func TestTodoContinuePredecessorJobSelection(t *testing.T) {
	t.Parallel()
	s, _ := newContinueService(t)
	seedContinuePlan(t, s, nil)
	seedContJob(t, s, "j-old", contPred, "sess-old", StatusDone, 100, nil)
	seedContJob(t, s, "j-new", contPred, "sess-new", StatusDone, 200, nil)
	seedContJob(t, s, "j-nosess", contPred, "", StatusDone, 250, nil)
	for i := range 130 {
		seedContJob(t, s, fmt.Sprintf("j-fail-%03d", i), contPred, "sess-f", StatusFailed, int64(300+i), nil)
	}
	got, ok, err := s.continuationPredecessorJob(contPred)
	if err != nil || !ok {
		t.Fatalf("continuationPredecessorJob: ok=%v err=%v", ok, err)
	}
	if got.ID != "j-new" {
		t.Fatalf("selected %s, want j-new (newest done job with a session)", got.ID)
	}
	if _, ok, _ := s.continuationPredecessorJob("nobody"); ok {
		t.Fatal("a todo with no jobs must have no predecessor job")
	}

	// A rework continuation of the same item is the newest done job and wins; its
	// session is resolved to the real agent through the exec carrier.
	seedContJob(t, s, "j-rework", contPred, "sess-new", StatusDone, 900, func(j *JobResult) {
		j.Agent = agent.ExecAgentKey
		j.ResumedFrom = "j-new"
		j.RequestJSON = `{"agent":"exec","cmd":["x"],"cwd":"."}`
	})
	got, _, _ = s.continuationPredecessorJob(contPred)
	if got.ID != "j-rework" {
		t.Fatalf("selected %s after rework, want j-rework", got.ID)
	}
	if reason := continueDecision(t, s, contSucc); reason != "" {
		t.Fatalf("reason = %q, want the exec carrier to resolve to its real agent and pass", reason)
	}
}

// TestTodoContinueClaim: condition 7 distinguishes a slot another item holds from one
// this item already holds, and never releases a slot it did not take.
func TestTodoContinueClaim(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) *Service {
		s, _ := newContinueService(t)
		seedContinuePlan(t, s, nil)
		seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, nil)
		return s
	}
	t.Run("held by another item", func(t *testing.T) {
		t.Parallel()
		s := setup(t)
		if ok, err := s.meta.ClaimTodoContinuation(contPred, "other-item"); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		if got := continueDecision(t, s, contSucc); got != continueClaimedByOther {
			t.Fatalf("reason = %q, want %q", got, continueClaimedByOther)
		}
		if holder := continuedBy(t, s, contPred); holder != "other-item" {
			t.Fatalf("continued_by = %q, the other item's claim must survive", holder)
		}
	})
	t.Run("held by this item", func(t *testing.T) {
		t.Parallel()
		s := setup(t)
		if ok, err := s.meta.ClaimTodoContinuation(contPred, contSucc); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		if got := continueDecision(t, s, contSucc); got != continueClaimedBySelf {
			t.Fatalf("reason = %q, want %q", got, continueClaimedBySelf)
		}
		if holder := continuedBy(t, s, contPred); holder != contSucc {
			t.Fatalf("continued_by = %q, the earlier continuation's claim must survive", holder)
		}
	})
	t.Run("two successors race for one predecessor", func(t *testing.T) {
		t.Parallel()
		s := setup(t)
		if err := s.Meta().InsertTodo(jobstore.PlanTodo{
			TodoID: "ts2", PlanID: contPlan, Title: "second succ", Assignee: "ca", ContinueFrom: contPred,
		}); err != nil {
			t.Fatal(err)
		}
		var (
			wg    sync.WaitGroup
			start = make(chan struct{})
			won   = make(chan string, 2)
		)
		for _, id := range []string{contSucc, "ts2"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if ok, err := s.meta.ClaimTodoContinuation(contPred, id); err == nil && ok {
					won <- id
				}
			}()
		}
		close(start)
		wg.Wait()
		close(won)
		var winners []string
		for id := range won {
			winners = append(winners, id)
		}
		if len(winners) != 1 {
			t.Fatalf("claim winners = %v, want exactly one", winners)
		}
		loser := contSucc
		if winners[0] == contSucc {
			loser = "ts2"
		}
		if got := continueDecision(t, s, loser); got != continueClaimedByOther {
			t.Fatalf("loser reason = %q, want %q", got, continueClaimedByOther)
		}
		if got := continueDecision(t, s, winners[0]); got != continueClaimedBySelf {
			t.Fatalf("winner reason = %q, want %q", got, continueClaimedBySelf)
		}
	})
}

// dispatched submits nothing of its own: it reads back the job a dispatch started.
func dispatchedJob(t *testing.T, s *Service, d TodoDispatch, err error) JobResult {
	t.Helper()
	if err != nil || d.Job == nil {
		t.Fatalf("dispatch: %+v err=%v", d, err)
	}
	final, ok := s.Wait(d.Job.ID)
	if !ok {
		t.Fatalf("Wait: job %s not found", d.Job.ID)
	}
	return final
}

func continueFallbackEvents(t *testing.T, s *Service) []jobstore.JobEvent {
	t.Helper()
	var out []jobstore.JobEvent
	for _, e := range todoEvents(t, s, PlanEventScope(contPlan)) {
		if e.Type == EventJobContinueFallback {
			out = append(out, e)
		}
	}
	return out
}

// TestTodoContinueFallbackDispatch: a todo whose conditions fail dispatches the ORDINARY request, stores why in continue_note, emits
// job.continue_fallback and holds no claim.
func TestTodoContinueFallbackDispatch(t *testing.T) {
	t.Parallel()
	t.Run("a condition fails", func(t *testing.T) {
		t.Parallel()
		s, _ := newContinueService(t)
		seedContinuePlan(t, s, nil)
		setTodoStatus(t, s, contPred, jobstore.TodoDoing) // condition 1
		d, err := s.DispatchTodo(contSucc, "")            // explicit dispatch needs condition 1 too
		final := dispatchedJob(t, s, d, err)
		if final.ResumedFrom != "" || final.SessionID != "" && strings.Contains(final.RequestJSON, "resumed_from") {
			t.Fatalf("job continued a session: %+v", final)
		}
		got, _, _ := s.meta.GetTodo(contSucc)
		if !strings.HasPrefix(got.ContinueNote, "未接续：") || !strings.Contains(got.ContinueNote, "未完成") ||
			!strings.HasSuffix(got.ContinueNote, "已开新会话") {
			t.Fatalf("continue_note = %q", got.ContinueNote)
		}
		if got.DispatchError != "" {
			t.Fatalf("dispatch_error = %q, a fallback is not a refused dispatch", got.DispatchError)
		}
		evs := continueFallbackEvents(t, s)
		if len(evs) != 1 || !strings.Contains(evs[0].Detail, `"todo_id":"`+contSucc+`"`) || !strings.Contains(evs[0].Detail, "未完成") {
			t.Fatalf("job.continue_fallback events = %+v", evs)
		}
	})
	t.Run("every condition holds", func(t *testing.T) {
		t.Parallel()
		s, _ := newContinueService(t)
		seedContinuePlan(t, s, nil)
		seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, nil)
		d, err := s.DispatchTodo(contSucc, "")
		final := dispatchedJob(t, s, d, err)
		if final.ResumedFrom != "j-pred" || final.SessionID != "sess-p" || final.TodoID != contSucc {
			t.Fatalf("job = resumed_from %q session %q todo %q, want a continuation of j-pred for %s",
				final.ResumedFrom, final.SessionID, final.TodoID, contSucc)
		}
		got, _, _ := s.meta.GetTodo(contSucc)
		if got.ContinueNote != "" {
			t.Fatalf("continue_note = %q, a continued item has nothing to explain", got.ContinueNote)
		}
		if holder := continuedBy(t, s, contPred); holder != contSucc {
			t.Fatalf("continued_by = %q, the claim must stay with the successor", holder)
		}
		if n := len(continueFallbackEvents(t, s)); n != 0 {
			t.Fatalf("job.continue_fallback events = %d, want none", n)
		}
		var continued []jobstore.JobEvent
		for _, e := range todoEvents(t, s, final.ID) {
			if e.Type == EventJobContinued {
				continued = append(continued, e)
			}
		}
		if len(continued) != 1 || !strings.Contains(continued[0].Detail, `"pred_todo":"`+contPred+`"`) ||
			!strings.Contains(continued[0].Detail, `"pred_job":"j-pred"`) || !strings.Contains(continued[0].Detail, `"chain":1`) {
			t.Fatalf("job.continued events = %+v", continued)
		}
	})
	t.Run("a todo without continue_from is untouched", func(t *testing.T) {
		t.Parallel()
		s, _ := newContinueService(t)
		seedContinuePlan(t, s, func(td *jobstore.PlanTodo) { td.ContinueFrom = "" })
		d, err := s.DispatchTodo(contSucc, "")
		dispatchedJob(t, s, d, err)
		got, _, _ := s.meta.GetTodo(contSucc)
		if got.ContinueNote != "" || len(continueFallbackEvents(t, s)) != 0 {
			t.Fatalf("continue_note=%q events=%d, a plain todo is never evaluated", got.ContinueNote, len(continueFallbackEvents(t, s)))
		}
	})
}

// TestTodoContinueDifferential: a todo with continue_from whose conditions fail
// produces the same request_json as one without it; the only difference on the todo is
// continue_note.
func TestTodoContinueDifferential(t *testing.T) {
	t.Parallel()
	s, _ := newContinueService(t)
	seedContinuePlan(t, s, func(td *jobstore.PlanTodo) {
		td.Title = "same item"
		td.Note = "same note"
		td.Acceptance = "criterion"
		td.Verify = []string{"true"}
		td.Model = "m-1"
	})
	// The twin: same fields, no continue_from, in its own plan.
	if err := s.Meta().InsertPlan(jobstore.Plan{
		PlanID: "pc2", Title: "Continue plan", ProjectKey: "self", Status: jobstore.PlanOpen, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Meta().InsertTodo(jobstore.PlanTodo{
		TodoID: "twin", PlanID: "pc2", Title: "same item", Note: "same note", Assignee: "ca",
		Acceptance: "criterion", Verify: []string{"true"}, Model: "m-1",
	}); err != nil {
		t.Fatal(err)
	}
	setTodoStatus(t, s, contPred, jobstore.TodoDoing) // condition 1 fails

	d1, err1 := s.DispatchTodo(contSucc, "")
	j1 := dispatchedJob(t, s, d1, err1)
	d2, err2 := s.DispatchTodo("twin", "")
	j2 := dispatchedJob(t, s, d2, err2)

	r1, r2 := requestOf(t, j1), requestOf(t, j2)
	for _, r := range []*JobRequest{&r1, &r2} {
		r.TodoID, r.PlanID, r.RequestID = "", "", ""
	}
	// The plan titles are the same by construction, so the default prompt matches too.
	j1b, _ := json.Marshal(r1)
	j2b, _ := json.Marshal(r2)
	if string(j1b) != string(j2b) {
		t.Fatalf("requests differ:\n continue_from: %s\n plain:         %s", j1b, j2b)
	}
	a, _, _ := s.meta.GetTodo(contSucc)
	b, _, _ := s.meta.GetTodo("twin")
	if a.ContinueNote == "" || b.ContinueNote != "" {
		t.Fatalf("continue_note = %q / %q, want it only on the todo with continue_from", a.ContinueNote, b.ContinueNote)
	}
}

// TestTodoContinueSubmitErrors: a continuation request refused with a continuation-class
// error gives the slot back, records the note and submits the ordinary request ONCE
// without a dispatch_error; any other refusal also gives the slot back but stays a
// dispatch_error. The slot is not released between the two attempts of the
// source-session retry.
func TestTodoContinueSubmitErrors(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (*Service, jobstore.PlanTodo, JobRequest, JobRequest) {
		s, _ := newContinueService(t)
		seedContinuePlan(t, s, nil)
		todo, _, _ := s.meta.GetTodo(contSucc)
		plan, _, _ := s.meta.GetPlan(contPlan)
		normal, why := s.normalTodoRequest(plan, todo, "")
		if why != "" {
			t.Fatalf("normalTodoRequest: %s", why)
		}
		cont := normal
		cont.ResumedFrom = "marker"
		if ok, err := s.meta.ClaimTodoContinuation(contPred, contSucc); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		return s, todo, normal, cont
	}
	for _, class := range []error{ErrNoSession, ErrCrossRunner, ErrResumeUnsupported, ErrJobNotTerminal} {
		t.Run("class "+class.Error(), func(t *testing.T) {
			t.Parallel()
			s, todo, normal, cont := setup(t)
			var submitted []string
			s.submitTodo = func(req JobRequest) (JobResult, error) {
				submitted = append(submitted, req.ResumedFrom)
				if req.ResumedFrom == "marker" {
					return JobResult{}, fmt.Errorf("%w: boom", class)
				}
				return s.Submit(req)
			}
			d, err := s.submitTodoRequest(todo, "self", cont, normal, true)
			dispatchedJob(t, s, d, err)
			if len(submitted) != 2 || submitted[0] != "marker" || submitted[1] != "" {
				t.Fatalf("submitted resumed_from = %q, want the continuation then the ordinary request", submitted)
			}
			if holder := continuedBy(t, s, contPred); holder != "" {
				t.Fatalf("continued_by = %q, the claim must be released", holder)
			}
			got, _, _ := s.meta.GetTodo(contSucc)
			if !strings.Contains(got.ContinueNote, "boom") || got.DispatchError != "" {
				t.Fatalf("continue_note=%q dispatch_error=%q", got.ContinueNote, got.DispatchError)
			}
			if len(continueFallbackEvents(t, s)) != 1 {
				t.Fatalf("want one job.continue_fallback event")
			}
		})
	}
	t.Run("another refusal is a dispatch error", func(t *testing.T) {
		t.Parallel()
		s, todo, normal, cont := setup(t)
		calls := 0
		s.submitTodo = func(JobRequest) (JobResult, error) {
			calls++
			return JobResult{}, fmt.Errorf("%w: nope", ErrInvalidRequest)
		}
		d, err := s.submitTodoRequest(todo, "self", cont, normal, true)
		if err != nil || d.Job != nil || !strings.Contains(d.Reason, "nope") {
			t.Fatalf("dispatch = %+v err=%v, want a refused dispatch", d, err)
		}
		if calls != 1 {
			t.Fatalf("submit calls = %d, want 1 (no ordinary retry for a non-continuation error)", calls)
		}
		if holder := continuedBy(t, s, contPred); holder != "" {
			t.Fatalf("continued_by = %q, the claim must be released", holder)
		}
		got, _, _ := s.meta.GetTodo(contSucc)
		if !strings.Contains(got.DispatchError, "nope") {
			t.Fatalf("dispatch_error = %q", got.DispatchError)
		}
	})
	t.Run("source session retry keeps the claim", func(t *testing.T) {
		t.Parallel()
		s, todo, normal, cont := setup(t)
		cont.SourceSessionID = "sup-session"
		var holders []string
		s.submitTodo = func(req JobRequest) (JobResult, error) {
			holders = append(holders, continuedBy(t, s, contPred))
			if req.SourceSessionID != "" {
				return JobResult{}, fmt.Errorf("%w: elsewhere", ErrSourceSessionElsewhere)
			}
			return JobResult{}, errors.New("stop here")
		}
		_, _ = s.submitTodoRequest(todo, "self", cont, normal, true)
		if len(holders) != 2 || holders[0] != contSucc || holders[1] != contSucc {
			t.Fatalf("holder at each submit attempt = %q, want the claim held through the retry", holders)
		}
		if holder := continuedBy(t, s, contPred); holder != "" {
			t.Fatalf("continued_by = %q after the final failure, want it released", holder)
		}
	})
}

// TestTodoContinueTriggerPaths: both ways a successor gets dispatched evaluate the
// continuation — the chain advance when the predecessor's job finishes, and the same
// advance after a reviewer accepts it.
func TestTodoContinueTriggerPaths(t *testing.T) {
	t.Parallel()
	for _, review := range []bool{false, true} {
		name := "auto chain on finish"
		if review {
			name = "after review accept"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _ := newContinueService(t)
			seedContinuePlan(t, s, func(td *jobstore.PlanTodo) { td.Auto = true })
			setTodoStatus(t, s, contPred, jobstore.TodoReady)
			final := submitAndWait(t, s, JobRequest{
				ProjectKey: "self", Agent: "ca", Runner: "local", Prompt: "pred work", Cwd: ".",
				TimeoutSec: 30, TodoID: contPred, SessionID: "sess-p", Review: review,
			})
			if review {
				if final.Status != StatusNeedsReview {
					t.Fatalf("pred = %s (%s), want needs_review", final.Status, final.Error)
				}
				if jobs, _ := s.meta.ListJobsByTodo(contSucc, 5); len(jobs) != 0 {
					t.Fatalf("successor dispatched before the review verdict")
				}
				if _, err := s.AcceptJob(final.ID, "alice", "ok"); err != nil {
					t.Fatalf("AcceptJob: %v", err)
				}
			} else if final.Status != StatusDone {
				t.Fatalf("pred = %s (%s), want done", final.Status, final.Error)
			}
			var succJob string
			wait.For(t, 10*time.Second, "the successor to be dispatched", func() (bool, any) {
				jobs, err := s.meta.ListJobsByTodo(contSucc, 5)
				if err == nil && len(jobs) == 1 {
					succJob = jobs[0].ID
					return true, nil
				}
				return false, err
			})
			sj, ok := s.Wait(succJob)
			if !ok || sj.ResumedFrom != final.ID {
				t.Fatalf("successor job = %+v ok=%v, want a continuation of %s", sj, ok, final.ID)
			}
			got, _, _ := s.meta.GetTodo(contSucc)
			if got.ContinueNote != "" || got.ContinuedBy != "" {
				t.Fatalf("continue_note = %q continued_by = %q", got.ContinueNote, got.ContinuedBy)
			}
			if pred, _, _ := s.meta.GetTodo(contPred); pred.ContinuedBy != contSucc {
				t.Fatalf("pred continued_by = %q, want the successor to hold the slot", pred.ContinuedBy)
			}
		})
	}
}

// TestRebuildTodoRequestBuildsTheItemsOwnRequest: the seam a failed continuation is
// taken over through is wired to the ordinary dispatch request of the item it is asked
// for — not the predecessor's, not a replay.
func TestRebuildTodoRequestBuildsTheItemsOwnRequest(t *testing.T) {
	t.Parallel()
	s, _ := newContinueService(t)
	seedContinuePlan(t, s, func(td *jobstore.PlanTodo) { td.Note = "succ note"; td.Acceptance = "succ criteria" })
	if s.rebuildTodoRequest == nil {
		t.Fatal("NewService must wire rebuildTodoRequest")
	}
	req, err := s.rebuildTodoRequest(contSucc)
	if err != nil {
		t.Fatalf("rebuildTodoRequest: %v", err)
	}
	todo, _, _ := s.meta.GetTodo(contSucc)
	plan, _, _ := s.meta.GetPlan(contPlan)
	want, why := s.normalTodoRequest(plan, todo, "")
	if why != "" {
		t.Fatal(why)
	}
	if fmt.Sprintf("%+v", req) != fmt.Sprintf("%+v", want) {
		t.Fatalf("rebuilt request differs from the ordinary one:\n got  %+v\n want %+v", req, want)
	}
	if req.TodoID != contSucc || req.PlanID != contPlan || !strings.Contains(req.Prompt, "succ note") ||
		strings.Contains(req.Prompt, "pred item") || req.ResumedFrom != "" || req.SessionID != "" {
		t.Fatalf("rebuilt request is not the item's own: %+v", req)
	}
	if _, err := s.rebuildTodoRequest("no-such-todo"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unknown todo: err = %v, want ErrInvalidRequest", err)
	}
	// An item that cannot be dispatched (exec without --cmd) is an error, not a request.
	if err := s.Meta().InsertTodo(jobstore.PlanTodo{TodoID: "no-cmd", PlanID: contPlan, Title: "x", Assignee: agent.ExecAgentKey}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rebuildTodoRequest("no-cmd"); err == nil {
		t.Fatal("exec item without --cmd must not rebuild")
	}
}

func TestContinueEventTypesRegistered(t *testing.T) {
	t.Parallel()
	if EventJobContinued != "job.continued" || EventJobContinueFallback != "job.continue_fallback" {
		t.Fatalf("event names = %q / %q", EventJobContinued, EventJobContinueFallback)
	}
}
