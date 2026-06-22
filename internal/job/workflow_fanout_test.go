package job

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// ---------------------------------------------------------------------------
// P2 fan-out test helpers
// ---------------------------------------------------------------------------

// fanEchoStep builds a fan-out exec step where EVERY fan succeeds (exit 0). fanOut is
// the parallelism; join is the aggregation policy ("" defaults to all).
func fanEchoStep(name string, fanOut int, join string) StepSpec {
	return StepSpec{
		Name: name, ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "echo " + name}, Cwd: ".", TimeoutSec: 30,
		FanOut: fanOut, Join: join,
	}
}

// fanOneFailsStep builds a fan-out step where EXACTLY ONE fan fails and the rest
// succeed, deterministically and race-free: each fan races to atomically `mkdir` a
// single claim dir; the ONE winner exits 7, every loser (dir already exists) exits 0.
// This makes "1 of N failed" reproducible regardless of fan scheduling order.
func fanOneFailsStep(name, dir string, fanOut int, join string) StepSpec {
	claim := filepath.Join(dir, "fanfail-"+name)
	// `mkdir CLAIM 2>/dev/null && exit 7 || exit 0`: mkdir is atomic — succeeds for the
	// first fan (exit 7), fails for every later fan (exit 0).
	script := "mkdir " + claim + " 2>/dev/null && exit 7 || exit 0"
	return StepSpec{
		Name: name, ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", script}, Cwd: ".", TimeoutSec: 30,
		FanOut: fanOut, Join: join,
	}
}

// fanAllFailStep builds a fan-out step where every fan fails (exit 7).
func fanAllFailStep(name string, fanOut int, join string) StepSpec {
	return StepSpec{
		Name: name, ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "exit 7"}, Cwd: ".", TimeoutSec: 30,
		FanOut: fanOut, Join: join,
	}
}

// fanJobsAtStep returns all jobs of the workflow at step_index==step (any attempt).
func fanJobsAtStep(jobs []jobstore.JobRecord, step int) []jobstore.JobRecord {
	out := make([]jobstore.JobRecord, 0)
	for _, j := range jobs {
		if j.StepIndex == step {
			out = append(out, j)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// T2.1 validateFanout — table-driven submit-time validation.
// ---------------------------------------------------------------------------

func TestValidateFanout(t *testing.T) {
	mk := func(fanOut int, join string) WorkflowSpec {
		st := echoStep("s")
		st.FanOut = fanOut
		st.Join = join
		return WorkflowSpec{Steps: []StepSpec{st}}
	}
	cases := []struct {
		name    string
		spec    WorkflowSpec
		wantErr bool
	}{
		{"v1 no fan no join", mk(0, ""), false},
		{"single job no join", mk(1, ""), false},
		{"fan all", mk(3, "all"), false},
		{"fan any", mk(3, "any"), false},
		{"fan quorum", mk(3, "quorum"), false},
		{"fan default join", mk(3, ""), false},
		{"fan at cap", mk(maxFanOut, "all"), false},
		{"negative fan_out", mk(-1, ""), true},
		{"fan over cap", mk(maxFanOut+1, "all"), true},
		{"unknown join", mk(3, "first"), true},
		{"join on single job", mk(1, "all"), true},
		{"join on zero fan", mk(0, "any"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFanout(tc.spec)
			if tc.wantErr && err == nil {
				t.Fatalf("validateFanout(%s) = nil, want error", tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateFanout(%s) = %v, want nil", tc.name, err)
			}
			if tc.wantErr {
				assertInvalidRequest(t, err)
			}
		})
	}
}

// TestSubmitWorkflowRejectsBadFanout proves validateFanout is wired into SubmitWorkflow.
func TestSubmitWorkflowRejectsBadFanout(t *testing.T) {
	s := newTestService(t, t.TempDir())
	bad := echoStep("over")
	bad.FanOut = maxFanOut + 1
	_, err := s.SubmitWorkflow(WorkflowSpec{Steps: []StepSpec{bad}}, "alice")
	if err == nil {
		t.Fatal("expected SubmitWorkflow to reject fan_out over the cap")
	}
	assertInvalidRequest(t, err)
}

// ---------------------------------------------------------------------------
// T2.2 fan-out start: N jobs, fan_index 1..N, distinct request_ids, C5 unique.
// ---------------------------------------------------------------------------

// TestFanOutStartsNJobs asserts a fan_out=3 step starts EXACTLY 3 jobs with fan_index
// 1..3, each carrying a distinct deterministic request_id "<wf>:s1:a1:fK" (C5 — no
// duplicate), and the all-success step completes the workflow done.
func TestFanOutStartsNJobs(t *testing.T) {
	s := newTestService(t, t.TempDir())
	wf, err := s.SubmitWorkflow(WorkflowSpec{
		Steps: []StepSpec{fanEchoStep("fan", 3, "all")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, s, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("fan_out all-success workflow = %s (err=%s), want done", final.Status, final.Error)
	}

	jobs, _ := s.meta.ListWorkflowJobs(wf.ID)
	step1 := fanJobsAtStep(jobs, 1)
	if len(step1) != 3 {
		t.Fatalf("step 1 has %d fan jobs, want 3", len(step1))
	}

	// fan_index 1..3 each appears exactly once; request_ids are distinct and follow the
	// "<wf>:s1:a1:fK" form (the C5 dedupe key — never duplicated).
	seenFan := map[int]int{}
	seenReq := map[string]int{}
	for _, j := range step1 {
		seenFan[j.FanIndex]++
		seenReq[j.RequestID]++
		if j.Status != StatusDone {
			t.Fatalf("fan %d = %s, want done", j.FanIndex, j.Status)
		}
		want := wf.ID + ":s1:a1:f" + itoa(j.FanIndex)
		if j.RequestID != want {
			t.Fatalf("fan %d request_id = %q, want %q", j.FanIndex, j.RequestID, want)
		}
	}
	for f := 1; f <= 3; f++ {
		if seenFan[f] != 1 {
			t.Fatalf("fan_index %d appeared %d times, want exactly 1 (C5)", f, seenFan[f])
		}
	}
	for req, c := range seenReq {
		if c != 1 {
			t.Fatalf("request_id %q used %d times, want 1 (C5 idempotency)", req, c)
		}
	}

	// A step.fanout event was recorded with all fan job_ids.
	if !hasWorkflowEvent(t, s, wf.ID, EventStepFanout) {
		t.Fatal("expected a step.fanout event for a fan-out step")
	}
}

// itoa is a tiny local int->string for request_id assembly in tests.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ---------------------------------------------------------------------------
// T2.3 join semantics — integration (all/any) + pure-helper (all/any/quorum).
// ---------------------------------------------------------------------------

// TestJoinAllOneFailsFailsStep: join=all, one fan fails → the whole step (and workflow)
// fails (fail-fast default on_failure).
func TestJoinAllOneFailsFailsStep(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	wf, err := s.SubmitWorkflow(WorkflowSpec{
		Steps: []StepSpec{fanOneFailsStep("a", root, 3, "all"), echoStep("never2")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, s, wf.ID)
	if final.Status != jobstore.WorkflowFailed {
		t.Fatalf("join=all 1-fail workflow = %s, want failed", final.Status)
	}
	jobs, _ := s.meta.ListWorkflowJobs(wf.ID)
	// step 2 must not run (fail-fast).
	if len(fanJobsAtStep(jobs, 2)) != 0 {
		t.Fatal("step 2 ran despite join=all step failure")
	}
	// step 1 started all 3 fans; exactly one failed.
	step1 := fanJobsAtStep(jobs, 1)
	if len(step1) != 3 {
		t.Fatalf("step 1 has %d fans, want 3", len(step1))
	}
	failed := 0
	for _, j := range step1 {
		if j.Status != StatusDone {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("step 1 had %d failed fans, want exactly 1", failed)
	}
}

// TestJoinAnyOneDoneAdvances: join=any, one fan fails but two succeed → the step is
// satisfied (≥1 done) and the workflow advances/completes done.
func TestJoinAnyOneDoneAdvances(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	wf, err := s.SubmitWorkflow(WorkflowSpec{
		Steps: []StepSpec{fanOneFailsStep("a", root, 3, "any"), echoStep("two")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, s, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("join=any 1-fail-2-done workflow = %s (err=%s), want done", final.Status, final.Error)
	}
	jobs, _ := s.meta.ListWorkflowJobs(wf.ID)
	// step 2 ran (the chain advanced past the any-satisfied step 1).
	if s2 := stepJobAttempt(jobs, 2, 1); s2 == nil || s2.Status != StatusDone {
		t.Fatalf("step 2 should run and be done after join=any advance, got %+v", s2)
	}
}

// TestJoinQuorumAllDoneAdvances: join=quorum, all 3 fans succeed (a majority) → advance.
func TestJoinQuorumAllDoneAdvances(t *testing.T) {
	s := newTestService(t, t.TempDir())
	wf, err := s.SubmitWorkflow(WorkflowSpec{
		Steps: []StepSpec{fanEchoStep("q", 3, "quorum"), echoStep("two")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, s, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("join=quorum all-done workflow = %s (err=%s), want done", final.Status, final.Error)
	}
}

// TestFanTerminalAndVerdict is the pure-helper proof of the join semantics over crafted
// fan job slices (deterministic, no scheduling): all/any/quorum terminal-readiness and
// done/failed verdict — the precise table the integration tests rely on.
func TestFanTerminalAndVerdict(t *testing.T) {
	job := func(status string) *jobstore.JobRecord { return &jobstore.JobRecord{Status: status} }
	done, fail, run := StatusDone, StatusFailed, StatusRunning

	cases := []struct {
		name         string
		fans         []*jobstore.JobRecord
		want         int
		join         string
		wantTerminal bool
		wantVerdict  string // only checked when wantTerminal
	}{
		// all
		{"all 3/3 done", []*jobstore.JobRecord{job(done), job(done), job(done)}, 3, joinAll, true, StatusDone},
		{"all 1 fail", []*jobstore.JobRecord{job(done), job(fail), job(done)}, 3, joinAll, true, StatusFailed},
		{"all 1 running", []*jobstore.JobRecord{job(done), job(run), job(done)}, 3, joinAll, false, ""},
		// any
		{"any 1 done early", []*jobstore.JobRecord{job(done), job(run), job(run)}, 3, joinAny, true, StatusDone},
		{"any all fail", []*jobstore.JobRecord{job(fail), job(fail), job(fail)}, 3, joinAny, true, StatusFailed},
		{"any none done yet", []*jobstore.JobRecord{job(fail), job(run), job(run)}, 3, joinAny, false, ""},
		// quorum (need >half: 3 -> 2, 4 -> 3)
		{"quorum 2/3 done", []*jobstore.JobRecord{job(done), job(done), job(run)}, 3, joinQuorum, true, StatusDone},
		{"quorum 1/3 done 2 fail", []*jobstore.JobRecord{job(done), job(fail), job(fail)}, 3, joinQuorum, true, StatusFailed},
		{"quorum 1 done 1 fail 1 run", []*jobstore.JobRecord{job(done), job(fail), job(run)}, 3, joinQuorum, false, ""},
		{"quorum 4 need 3: 2 done 1 run 1 run", []*jobstore.JobRecord{job(done), job(done), job(run), job(run)}, 4, joinQuorum, false, ""},
		{"quorum 4 need 3: 3 done", []*jobstore.JobRecord{job(done), job(done), job(done), job(run)}, 4, joinQuorum, true, StatusDone},
		{"quorum 4 need 3: 2 fail impossible", []*jobstore.JobRecord{job(done), job(fail), job(fail), job(run)}, 4, joinQuorum, true, StatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTerm := fanTerminal(tc.fans, tc.want, tc.join)
			if gotTerm != tc.wantTerminal {
				t.Fatalf("fanTerminal = %v, want %v", gotTerm, tc.wantTerminal)
			}
			if tc.wantTerminal {
				if gv := fanVerdict(tc.fans, tc.want, tc.join); gv != tc.wantVerdict {
					t.Fatalf("fanVerdict = %s, want %s", gv, tc.wantVerdict)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// fan-out + retry: a failing fan-out step retries the WHOLE step (attempt+1, N fans).
// ---------------------------------------------------------------------------

// TestFanOutRetryReRunsAllFans: a fan_out=3 step that fails (all fans) with
// on_failure=retry re-runs the ENTIRE step at attempt 2 (a fresh set of 3 fans), then
// exhausts and fails. Asserts attempt 2 started 3 NEW fan jobs (whole-step retry).
func TestFanOutRetryReRunsAllFans(t *testing.T) {
	s := newTestService(t, t.TempDir())
	step := fanAllFailStep("rfan", 3, "all")
	step.OnFailure = onFailureRetry
	step.Retry = &RetryPolicy{MaxAttempts: 2, BackoffSec: []int{0}} // immediate retry, 2 attempts
	wf, err := s.SubmitWorkflow(WorkflowSpec{Steps: []StepSpec{step}}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}
	final := waitWorkflow(t, s, wf.ID)
	if final.Status != jobstore.WorkflowFailed {
		t.Fatalf("fan-out retry-exhausted workflow = %s, want failed", final.Status)
	}
	jobs, _ := s.meta.ListWorkflowJobs(wf.ID)
	// attempt 1: 3 fans, attempt 2: 3 fans = 6 jobs total at step 1.
	a1 := stepFanJobs(jobs, 1, 1)
	a2 := stepFanJobs(jobs, 1, 2)
	if len(a1) != 3 {
		t.Fatalf("attempt 1 had %d fans, want 3", len(a1))
	}
	if len(a2) != 3 {
		t.Fatalf("attempt 2 had %d fans, want 3 (whole-step retry re-ran all fans)", len(a2))
	}
	// Every (step,attempt,fan) is unique (no double-start under the retry+fan storm).
	seen := map[[3]int]int{}
	for _, j := range jobs {
		att := j.Attempt
		if att == 0 {
			att = 1
		}
		seen[[3]int{j.StepIndex, att, j.FanIndex}]++
	}
	for key, c := range seen {
		if c != 1 {
			t.Fatalf("(step %d, attempt %d, fan %d) started %d times, want 1", key[0], key[1], key[2], c)
		}
	}
	// A step.retry event was recorded.
	if !hasWorkflowEvent(t, s, wf.ID, EventStepRetry) {
		t.Fatal("expected a step.retry event for fan-out retry")
	}
}

// ---------------------------------------------------------------------------
// ⭐ Concurrency hard test: fan-out terminal storm must advance exactly once.
// ---------------------------------------------------------------------------

// TestFanOutConcurrentAdvanceOnce is the P2 ⭐ correctness test (plan 并发硬测): after a
// fan-out step's fans all reach terminal, MANY concurrent advanceWorkflow calls (the
// finish hooks of N fans + the sweeper + duplicates firing together) must aggregate and
// advance the chain EXACTLY ONCE — never start the next step twice, never double-start a
// fan, never advance the (step,attempt) pointer twice. Proves the fan aggregation +
// AdvanceStep二元组抢权 + deterministic request_id hold under the fan terminal storm.
func TestFanOutConcurrentAdvanceOnce(t *testing.T) {
	s := newTestService(t, t.TempDir())
	wf, err := s.SubmitWorkflow(WorkflowSpec{
		Steps: []StepSpec{fanEchoStep("fan", 3, "all"), echoStep("two"), echoStep("three")},
	}, "alice")
	if err != nil {
		t.Fatalf("SubmitWorkflow: %v", err)
	}

	// Wait for ALL 3 fans of step 1 to reach terminal (done), so the hammer races a
	// fully-decidable generation.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		jobs, _ := s.meta.ListWorkflowJobs(wf.ID)
		fans := stepFanJobs(jobs, 1, 1)
		allTerm := len(fans) == 3
		for _, j := range fans {
			if !isTerminal(j.Status) {
				allTerm = false
			}
		}
		if allTerm {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// HAMMER advanceWorkflow concurrently (finish-of-3-fans + sweeper + duplicates).
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.advanceWorkflow(wf.ID) }()
	}
	wg.Wait()

	final := waitWorkflow(t, s, wf.ID)
	if final.Status != jobstore.WorkflowDone {
		t.Fatalf("fan-out concurrent workflow = %s (err=%s), want done", final.Status, final.Error)
	}

	// CRITICAL invariant: no (step, attempt, fan) started more than once across the storm.
	jobs, _ := s.meta.ListWorkflowJobs(wf.ID)
	seen := map[[3]int]int{}
	for _, j := range jobs {
		att := j.Attempt
		if att == 0 {
			att = 1
		}
		seen[[3]int{j.StepIndex, att, j.FanIndex}]++
	}
	for key, c := range seen {
		if c != 1 {
			t.Fatalf("(step %d, attempt %d, fan %d) started %d times, want exactly 1 (幂等 violated)", key[0], key[1], key[2], c)
		}
	}
	// Step 1 has exactly 3 fan jobs (the storm did not spawn extra fans); steps 2 and 3
	// each have exactly one (single-job) — the chain advanced once per step.
	if n := len(fanJobsAtStep(jobs, 1)); n != 3 {
		t.Fatalf("step 1 has %d fan jobs, want 3", n)
	}
	if n := len(fanJobsAtStep(jobs, 2)); n != 1 {
		t.Fatalf("step 2 has %d jobs, want 1", n)
	}
	if n := len(fanJobsAtStep(jobs, 3)); n != 1 {
		t.Fatalf("step 3 has %d jobs, want 1", n)
	}
}
