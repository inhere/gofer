package job

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// gofer-s2er: a job must be able to learn its own limit. These tests cover the two
// injected variables (with / without a limit, after the project ceiling, on a
// continuation), the one-line notice appended to a batch agent job's prompt (exactly
// once, at the end, never for an exec argv) and the shapes that get neither.

// deadlineTestNow is the frozen instant these tests hang the clock on: an exact
// 08:00:00 UTC, so the rendered clock time and offset are assertable.
var deadlineTestNow = time.Unix(1_800_000_000, 0).In(time.UTC)

// newDeadlineService builds a Service with a frozen clock, a cli-agent whose argv prints
// what it received ("argv" echoes the arguments; the prompt is one of them), an
// env-printing exec-friendly agent and a project whose job-timeout ceiling is 120s —
// the setup the clamp assertions need. The clock is frozen AFTER the service exists, so
// every deadline these tests assert is exactly clock+limit.
func newDeadlineService(t *testing.T, root string, clk *fakeClock) *Service {
	t.Helper()
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec", "cli", "codex"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
				MaxTimeoutSec:  120,
			},
		},
		Agents: map[string]config.AgentConfig{
			"cli": {
				Type:    agent.TypeCLIAgent,
				Command: bin,
				Args:    []string{"argv", "{{prompt}}"},
			},
			"codex": {
				Type:          agent.TypeCLIAgent,
				Command:       bin,
				Args:          []string{"env-print", jobTimeoutSecEnv, jobDeadlineEnv},
				SessionResume: []string{"env-print", jobTimeoutSecEnv, jobDeadlineEnv},
			},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	clk.unix = deadlineTestNow.Unix()
	s.nowFn = clk.now
	return s
}

// TestJobTimeLimitResolvesShape pins the rules of the limit a job is told about: the
// admitted timeout for an ordinary job, max_session for a resident session (whose own
// timeout only bounds a single turn), the absolute end a recovered session already
// decided, and "no limit" for a job that has none.
func TestJobTimeLimitResolvesShape(t *testing.T) {
	t.Parallel()
	perTurn := 20 * time.Minute
	cases := []struct {
		name       string
		timeout    time.Duration
		req        JobRequest
		recovered  int64
		wantSec    int64
		wantOffset time.Duration
		noDeadline bool
	}{
		{name: "batch job", timeout: perTurn, wantSec: 1200, wantOffset: perTurn},
		{name: "batch job without a limit", timeout: 0, wantSec: 0},
		{
			name: "session bounded by max_session", timeout: perTurn,
			req:     JobRequest{Session: true, MaxSessionSec: 600},
			wantSec: 600, wantOffset: 10 * time.Minute,
		},
		{
			name: "session without max_session reports one turn and no deadline", timeout: perTurn,
			req:     JobRequest{Session: true},
			wantSec: 1200, noDeadline: true,
		},
		{
			name: "recovered session keeps the end its row decided", timeout: perTurn,
			req: JobRequest{Session: true, MaxSessionSec: 600}, recovered: 1_800_000_900,
			wantSec: 600, wantOffset: 900 * time.Second,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := jobTimeLimit(deadlineTestNow, tc.timeout, &tc.req, tc.recovered)
			if got.Sec != tc.wantSec {
				t.Fatalf("limit sec = %d, want %d", got.Sec, tc.wantSec)
			}
			if tc.wantSec == 0 || tc.noDeadline {
				if !got.Deadline.IsZero() {
					t.Fatalf("this limit must have no deadline, got %s", got.Deadline)
				}
				return
			}
			if want := deadlineTestNow.Add(tc.wantOffset); !got.Deadline.Equal(want) {
				t.Fatalf("deadline = %s, want %s", got.Deadline, want)
			}
		})
	}
}

// TestGoferJobEnvInjectsDeadline covers the variable side of the injection: both
// variables for a job with a limit (the deadline as RFC3339 with the job machine's own
// offset) and NEITHER of them for a job without one — an always-present variable
// pointing nowhere would read as a limit that does not exist.
func TestGoferJobEnvInjectsDeadline(t *testing.T) {
	t.Parallel()
	deadline := deadlineTestNow.Add(30 * time.Minute)
	env := goferJobEnv(map[string]string{"FOO": "bar"}, "job-1", "/work/cwd", "/res", jobLimit{Sec: 1800, Deadline: deadline})

	if env["FOO"] != "bar" || env["GOFER_JOB_ID"] != "job-1" {
		t.Fatalf("base metadata lost: %v", env)
	}
	if env[jobTimeoutSecEnv] != "1800" {
		t.Fatalf("%s = %q, want 1800", jobTimeoutSecEnv, env[jobTimeoutSecEnv])
	}
	raw := env[jobDeadlineEnv]
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("%s = %q is not RFC3339: %v", jobDeadlineEnv, raw, err)
	}
	if !parsed.Equal(deadline) {
		t.Fatalf("%s = %s, want %s", jobDeadlineEnv, parsed, deadline)
	}
	if _, offset := parsed.Zone(); offset != 0 {
		t.Fatalf("%s = %q must carry the zone offset", jobDeadlineEnv, raw)
	}

	none := goferJobEnv(map[string]string{}, "job-2", "/work/cwd", "/res", jobLimit{})
	if v, ok := none[jobTimeoutSecEnv]; ok {
		t.Fatalf("a job without a limit must get no %s, got %q", jobTimeoutSecEnv, v)
	}
	if v, ok := none[jobDeadlineEnv]; ok {
		t.Fatalf("a job without a limit must get no %s, got %q", jobDeadlineEnv, v)
	}
}

// TestGoferJobEnvSecondsOnlyForUnboundedSession: a resident session without max_session
// is told its per-turn seconds but no deadline (one fixed at start would go stale).
func TestGoferJobEnvSecondsOnlyForUnboundedSession(t *testing.T) {
	t.Parallel()
	env := goferJobEnv(nil, "job-1", "/work/cwd", "/res", jobLimit{Sec: 1200})
	if got := env[jobTimeoutSecEnv]; got != "1200" {
		t.Fatalf("%s = %q, want 1200", jobTimeoutSecEnv, got)
	}
	if v, ok := env[jobDeadlineEnv]; ok {
		t.Fatalf("an unbounded session must get no %s, got %q", jobDeadlineEnv, v)
	}
}

// TestJobDeadlineEnvReportsClampedTimeout is the end-to-end proof of what the two
// variables mean: the child of a job that ASKED for an hour under a 120s ceiling sees
// the admitted minute, and a deadline one minute past the job's own start — not the
// hour the request named.
func TestJobDeadlineEnvReportsClampedTimeout(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{}
	root := t.TempDir()
	s := newDeadlineService(t, root, clk)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "env-print", jobTimeoutSecEnv, jobDeadlineEnv), Cwd: ".",
		TimeoutSec: 3600,
	})
	if final.Status != StatusDone {
		t.Fatalf("job status = %s (err=%s)", final.Status, final.Error)
	}
	if final.TimeoutSec != 120 || !final.TimeoutClamped {
		t.Fatalf("admitted timeout = %d clamped=%v, want the project ceiling 120 and a clamp report",
			final.TimeoutSec, final.TimeoutClamped)
	}
	values := parseEnvPrint(readJobLog(t, final, store.StdoutFile))
	if values[jobTimeoutSecEnv] != "120" {
		t.Fatalf("child %s = %q, want the clamped 120", jobTimeoutSecEnv, values[jobTimeoutSecEnv])
	}
	parsed, err := time.Parse(time.RFC3339, values[jobDeadlineEnv])
	if err != nil {
		t.Fatalf("child %s = %q is not RFC3339: %v", jobDeadlineEnv, values[jobDeadlineEnv], err)
	}
	if want := time.Unix(clk.unix+120, 0); !parsed.Equal(want) {
		t.Fatalf("child %s = %s, want start+120s = %s", jobDeadlineEnv, parsed, want)
	}
}

// TestBatchAgentPromptGetsDeadlineLineOnce asserts the one-line notice in the prompt the
// agent actually received (the cli-agent's argv echoes the rendered prompt): exactly one
// line, at the end, naming the effective limit and — gofer-drlm — the moment it starts
// counting, with the absolute end left to the env var instead of a wall clock fixed at
// submit.
func TestBatchAgentPromptGetsDeadlineLineOnce(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{}
	root := t.TempDir()
	s := newDeadlineService(t, root, clk)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "cli", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 600,
	})
	if final.Status != StatusDone {
		t.Fatalf("job status = %s (err=%s)", final.Status, final.Error)
	}
	// The project ceiling (120s = 2 分钟) is what the agent has to work with, so the
	// notice quotes the clamped limit — not the 10 minutes the request asked for.
	if final.TimeoutSec != 120 {
		t.Fatalf("admitted timeout = %d, want the project ceiling 120", final.TimeoutSec)
	}
	out := readJobLog(t, final, store.StdoutFile)
	if got := strings.Count(out, deadlineLinePrefix); got != 1 {
		t.Fatalf("prompt carries %d deadline lines, want exactly 1:\n%s", got, out)
	}
	want := "本 job 时限 2 分钟，从开始执行算起（排队等锁的时间不计入）；截止时刻与环境变量 $GOFER_JOB_DEADLINE 一致"
	if !strings.Contains(out, want) {
		t.Fatalf("prompt is missing %q:\n%s", want, out)
	}
	// No wall clock from the submit moment: a queued job would read one that already lost
	// its queue time (gofer-drlm).
	if clock := time.Unix(clk.unix+120, 0).Format("15:04"); strings.Contains(out, "截止 "+clock) {
		t.Fatalf("the prompt line names the submit-time clock %s instead of the start-relative limit:\n%s", clock, out)
	}
	// LAST line of the prompt: the argv echo prints the prompt as its final argument.
	if !strings.HasSuffix(strings.TrimRight(out, " \t\r\n"), "接近时限时先提交已完成的部分，汇报里写清未完成项。") {
		t.Fatalf("the deadline line is not the last thing the agent read:\n%s", out)
	}
}

// TestExecJobGetsNoDeadlineLine: an exec job's argv is a command, not a prompt — it gets
// the two variables (they are metadata, not text) and no notice.
func TestExecJobGetsNoDeadlineLine(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{}
	root := t.TempDir()
	s := newDeadlineService(t, root, clk)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "argv", "hello"), Cwd: ".", Prompt: "do the thing", TimeoutSec: 600,
	})
	if final.Status != StatusDone {
		t.Fatalf("job status = %s (err=%s)", final.Status, final.Error)
	}
	if out := readJobLog(t, final, store.StdoutFile); strings.Contains(out, deadlineLinePrefix) {
		t.Fatalf("an exec argv must carry no deadline line:\n%s", out)
	}
}

// TestAppendJobDeadlineLineShapes covers the shapes that get no line (and the "already
// appended" marker): a line that enumerates the guards would otherwise re-add itself on
// every rerun of a persisted prompt.
func TestAppendJobDeadlineLineShapes(t *testing.T) {
	t.Parallel()
	bin := testcmd.Path(t)
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"cli":  {Type: agent.TypeCLIAgent, Command: bin},
		"exec": {Type: agent.TypeExec, Command: bin},
	}}
	limit := jobLimit{Sec: 600, Deadline: deadlineTestNow.Add(10 * time.Minute)}
	line := jobDeadlineLine(limit)

	cases := []struct {
		name      string
		req       JobRequest
		limit     jobLimit
		prompt    string
		wantCount int
	}{
		{name: "batch cli-agent", req: JobRequest{Agent: "cli"}, limit: limit, prompt: "task", wantCount: 1},
		{name: "no limit", req: JobRequest{Agent: "cli"}, prompt: "task"},
		{name: "exec argv", req: JobRequest{Agent: "exec"}, limit: limit, prompt: "task"},
		{name: "interactive pty", req: JobRequest{Agent: "cli", Interactive: true}, limit: limit, prompt: "task"},
		{name: "resident session", req: JobRequest{Agent: "cli", Session: true}, limit: limit, prompt: "task"},
		{name: "steward", req: JobRequest{Agent: "cli", Steward: true}, limit: limit, prompt: "task"},
		{name: "messenger relay", req: JobRequest{Agent: "cli", MessengerMeta: &MessengerMeta{}}, limit: limit, prompt: "task"},
		{name: "messenger dispatch", req: JobRequest{Agent: "cli", Messenger: &runner.MessengerDispatch{}}, limit: limit, prompt: "task"},
		{name: "continuation", req: JobRequest{Agent: "cli", ResumedFrom: "job-1"}, limit: limit, prompt: "task"},
		{name: "continuation of a source agent", req: JobRequest{Agent: "cli", ResumeSourceAgent: "codex"}, limit: limit, prompt: "task"},
		{name: "prompt already composed (dispatch re-entry)", req: JobRequest{Agent: "cli", RulesResolved: true}, limit: limit, prompt: "task"},
		// The marker case: a prompt that already carries the line keeps the ONE copy it has
		// (a rerun / peer re-submit of a persisted prompt must not stack a second).
		{name: "already appended", req: JobRequest{Agent: "cli"}, limit: limit, prompt: "task\n\n" + line, wantCount: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := appendJobDeadlineLine(cfg, &tc.req, tc.prompt, tc.limit)
			if count := strings.Count(got, deadlineLinePrefix); count != tc.wantCount {
				t.Fatalf("prompt = %q, want %d deadline line(s)", got, tc.wantCount)
			}
			if tc.wantCount == 1 && got != tc.prompt && got != tc.prompt+"\n\n"+line {
				t.Fatalf("prompt = %q, want the line appended after a blank line", got)
			}
		})
	}
}

// TestJobDeadlineLineRendersDurationAndEnv pins the text the agent reads, including the
// seconds fallback for a limit that is not a whole minute.
func TestJobDeadlineLineRendersDurationAndEnv(t *testing.T) {
	t.Parallel()
	got := jobDeadlineLine(jobLimit{Sec: 1800, Deadline: deadlineTestNow.Add(30 * time.Minute)})
	want := "本 job 时限 30 分钟，从开始执行算起（排队等锁的时间不计入）；截止时刻与环境变量 $GOFER_JOB_DEADLINE 一致，也可运行 gofer job show $GOFER_JOB_ID 查看。接近时限时先提交已完成的部分，汇报里写清未完成项。"
	if got != want {
		t.Fatalf("line = %q\nwant %q", got, want)
	}
	if got := limitText(90); got != "90 秒" {
		t.Fatalf("limitText(90) = %q, want 90 秒", got)
	}
}

// TestJobDeadlineEnvContinuationUsesItsOwnLimit: a continuation is governed like the run
// it continues (it inherits the AFFECTED source timeout) but its deadline is its own —
// start + limit — so a session resumed an hour later is not told a deadline that already
// passed. The source and the continuation print the variables their child saw.
func TestJobDeadlineEnvContinuationUsesItsOwnLimit(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{}
	root := t.TempDir()
	s := newDeadlineService(t, root, clk)

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "first", Cwd: ".", TimeoutSec: 600,
		CallerID: "alice", SessionID: "sess-codex",
	})
	if src.Status != StatusDone {
		t.Fatalf("source status = %s (err=%s)", src.Status, src.Error)
	}
	srcValues := parseEnvPrint(readJobLog(t, src, store.StdoutFile))
	// The source asked for 600s under this project's 120s ceiling, so 120 is the limit the
	// continuation inherits — the admitted one, not the requested one.
	if srcValues[jobTimeoutSecEnv] != "120" {
		t.Fatalf("source %s = %q, want the clamped 120", jobTimeoutSecEnv, srcValues[jobTimeoutSecEnv])
	}

	clk.advance(3600) // the caller comes back an hour later
	cont, err := s.ResumeJob(src.ID, "continue", "", "caller-1")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	final, ok := s.WaitFor(cont.ID, wait.Timeout(t, 10*time.Second))
	if !ok || final.Status != StatusDone {
		t.Fatalf("continuation %s status = %s (err=%s)", cont.ID, final.Status, final.Error)
	}
	values := parseEnvPrint(readJobLog(t, final, store.StdoutFile))
	if values[jobTimeoutSecEnv] != "120" {
		t.Fatalf("continuation %s = %q, want the inherited 120", jobTimeoutSecEnv, values[jobTimeoutSecEnv])
	}
	parsed, err := time.Parse(time.RFC3339, values[jobDeadlineEnv])
	if err != nil {
		t.Fatalf("continuation %s = %q is not RFC3339: %v", jobDeadlineEnv, values[jobDeadlineEnv], err)
	}
	if want := time.Unix(clk.unix+120, 0); !parsed.Equal(want) {
		t.Fatalf("continuation %s = %s, want its own start+120s = %s", jobDeadlineEnv, parsed, want)
	}
	if srcDeadline, _ := time.Parse(time.RFC3339, srcValues[jobDeadlineEnv]); parsed.Equal(srcDeadline) {
		t.Fatalf("continuation deadline %s equals the source's — the clock did not advance", parsed)
	}
}

// TestJobDeadlineStartsWhenTheJobStartsRunning is the gofer-drlm regression: a job that
// queues for a long time must hand its agent a deadline measured from the moment it STARTS
// RUNNING, not from its submit. A holder job owns the directory lock for a moment and the
// waiter parks in waiting_dir behind it — the queue F12 keeps out of the run — while the
// fake clock moves 30 minutes forward, so the two answers are far apart and nothing sleeps
// for the queue itself.
func TestJobDeadlineStartsWhenTheJobStartsRunning(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{}
	root := t.TempDir()
	s := newDeadlineService(t, root, clk)

	exclusive := true
	holder := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "sleep", "1s"), Cwd: ".", TimeoutSec: 60, ExclusiveDir: &exclusive,
	})
	waitForStatus(t, s, holder.ID, StatusRunning, 10*time.Second)

	waiter := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "env-print", jobTimeoutSecEnv, jobDeadlineEnv), Cwd: ".",
		TimeoutSec: 600, ExclusiveDir: &exclusive,
	})
	waitForStatus(t, s, waiter.ID, StatusWaitingDir, 10*time.Second)

	queued := int64(1800) // 30 minutes on the directory lock
	clk.advance(queued)

	final := waitStatus(t, s, waiter.ID, 30*time.Second, StatusDone, StatusFailed, StatusTimeout, StatusCancelled)
	if final.Status != StatusDone {
		t.Fatalf("job %s status = %s (err=%s), want done", waiter.ID, final.Status, final.Error)
	}
	values := parseEnvPrint(readJobLog(t, final, store.StdoutFile))
	if values[jobTimeoutSecEnv] != "120" {
		t.Fatalf("child %s = %q, want the admitted 120", jobTimeoutSecEnv, values[jobTimeoutSecEnv])
	}
	parsed, err := time.Parse(time.RFC3339, values[jobDeadlineEnv])
	if err != nil {
		t.Fatalf("child %s = %q is not RFC3339: %v", jobDeadlineEnv, values[jobDeadlineEnv], err)
	}
	if want := time.Unix(deadlineTestNow.Unix()+queued+120, 0); !parsed.Equal(want) {
		t.Fatalf("child %s = %s, want start+120s = %s (submit+120s = %s would be the old behaviour)",
			jobDeadlineEnv, parsed, want, time.Unix(deadlineTestNow.Unix()+120, 0))
	}
}

// TestRecoveredSessionKeepsMaxSessionDeadline goes through the resolution the recovery
// path uses (jobTimeLimit over a row's session state): a resident session restarted by
// serve keeps the absolute end its row decided, instead of being handed a whole new
// max_session window from the recovery moment.
func TestRecoveredSessionKeepsMaxSessionDeadline(t *testing.T) {
	t.Parallel()
	// The row a serve restart reads back: session state (max_session 600s, its end
	// decided 100s after the frozen start) plus the request that produced it.
	src := JobResult{
		Session: true, TimeoutSec: 1200, MaxSessionSec: 600,
		MaxSessionDeadlineAt: deadlineTestNow.Unix() + 100,
	}
	rec := fromRecord(jobstore.JobRecord{
		ID: "job-session", ProjectKey: "self", Agent: "acpsess", Runner: "local", Cwd: ".",
		TimeoutSec:       src.TimeoutSec,
		SessionStateJSON: marshalSessionState(src),
		RequestJSON:      `{"projectKey":"self","agent":"acpsess","runner":"local","session":true,"timeout_sec":1200,"max_session_sec":600}`,
	})
	if !rec.Session || rec.MaxSessionSec != 600 || rec.MaxSessionDeadlineAt == 0 {
		t.Fatalf("setup: session state did not round-trip: session=%v max_session=%d deadline=%d",
			rec.Session, rec.MaxSessionSec, rec.MaxSessionDeadlineAt)
	}

	recoveredAt := deadlineTestNow.Add(300 * time.Second) // serve comes back 300s later
	limit := jobTimeLimit(recoveredAt, time.Duration(rec.TimeoutSec)*time.Second,
		&JobRequest{Session: rec.Session, MaxSessionSec: rec.MaxSessionSec}, rec.MaxSessionDeadlineAt)

	if limit.Sec != 600 {
		t.Fatalf("recovered limit = %d, want max_session 600", limit.Sec)
	}
	if want := time.Unix(rec.MaxSessionDeadlineAt, 0); !limit.Deadline.Equal(want) {
		t.Fatalf("recovered deadline = %s, want the row's %s", limit.Deadline, want)
	}
	if extended := recoveredAt.Add(600 * time.Second); limit.Deadline.Equal(extended) {
		t.Fatalf("deadline was recomputed from the recovery moment (%s), extending the session", limit.Deadline)
	}
}
