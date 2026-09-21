package job

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// fakeClock drives a Service's nowFn so a timer test can jump to "the moment it is
// due" without sleeping. Real time is still used for the polling deadlines below
// (the two clocks answer different questions).
type fakeClock struct{ unix int64 }

func (c *fakeClock) now() time.Time    { return time.Unix(c.unix, 0) }
func (c *fakeClock) advance(sec int64) { c.unix += sec }

// newWakeupService builds a Service with the agents the wakeup tests need and a
// frozen clock:
//   - "codex": a resumable cli-agent (SessionResume renders testcmd's printf) — the
//     RESUME path of a fire;
//   - "plain": a cli-agent with no session machinery at all — a job of it can never
//     be resumed, which is the REBUILD path;
//   - "slow":  resumable, but its continuation sleeps, so a fire can be observed
//     while it is still in flight (the coalesce window).
func newWakeupService(t *testing.T, root string, clk *fakeClock) *Service {
	t.Helper()
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec", "codex", "plain", "slow"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Type:          agent.TypeCLIAgent,
				Command:       bin,
				Args:          []string{"stderr-exit", "0", "", "{{prompt}}"},
				SessionResume: []string{"printf", "resumed {{session_id}}: {{prompt}}"},
			},
			"plain": {
				Type:    agent.TypeCLIAgent,
				Command: bin,
				Args:    []string{"argv", "{{prompt}}"},
			},
			"slow": {
				Type:          agent.TypeCLIAgent,
				Command:       bin,
				Args:          []string{"stderr-exit", "0", "", "{{prompt}}"},
				SessionResume: []string{"sleep", "20s"},
			},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	clk.unix = 1_800_000_000
	s.nowFn = clk.now
	return s
}

// finishedSessionJob submits a cli-agent job with an explicit session id and waits
// for it to finish — the shape every wakeup test registers against (a finished job
// that CAN be resumed).
func finishedSessionJob(t *testing.T, s *Service, agentKey, prompt string) JobResult {
	t.Helper()
	return submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: agentKey, Runner: "local",
		Prompt: prompt, Cwd: ".", TimeoutSec: 30,
		CallerID:  "alice",
		SessionID: "sess-" + agentKey,
	})
}

// waitWakeupFired polls a wakeup row until it has fired at least want times. The
// EVENT path fires from the observer's own goroutine (a job's terminal transition
// completes first), so a test must wait for the row instead of assuming the fire
// already happened.
func waitWakeupFired(t *testing.T, s *Service, id string, want int64) jobstore.WakeupRecord {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		w, ok, err := s.GetWakeup(id)
		if err != nil {
			t.Fatalf("GetWakeup(%s): %v", id, err)
		}
		if ok && w.FiredCount >= want && w.ContinuationJobID != "" && !w.IsContinuationPending() {
			return w
		}
		if time.Now().After(deadline) {
			t.Fatalf("wakeup %s never fired %d time(s): %+v", id, want, w)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// eventDetailOf returns the decoded detail of the FIRST event of type want on a job.
func eventDetailOf(t *testing.T, s *Service, jobID, want string) map[string]any {
	t.Helper()
	evs, err := s.ListJobEvents(jobID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	for _, e := range evs {
		if e.Type != want {
			continue
		}
		var detail map[string]any
		if e.Detail == "" {
			return detail
		}
		if err := json.Unmarshal([]byte(e.Detail), &detail); err != nil {
			t.Fatalf("decode %s detail: %v", want, err)
		}
		return detail
	}
	t.Fatalf("job %s has no %s event (have %v)", jobID, want, eventTypes(t, s, jobID))
	return nil
}

// TestWakeupAtFiresResume: a timer that comes due resumes the finished job it was
// registered on — the continuation is a NEW job carrying the wakeup's tag, the
// target records job.wakeup_fired, and a once-mode wakeup is consumed (disabled)
// while re-enabling it arms it again.
func TestWakeupAtFiresResume(t *testing.T) {
	root := t.TempDir()
	clk := &fakeClock{}
	s := newWakeupService(t, root, clk)

	src := finishedSessionJob(t, s, "codex", "first turn")
	if src.Status != StatusDone || src.SessionID == "" {
		t.Fatalf("source job = %+v, want a finished job with a session", src)
	}

	w, err := s.CreateWakeup(WakeupSpec{
		JobID: src.ID, Kind: jobstore.WakeupKindAt, At: clk.unix + 600,
		Instruction: "check the CI result and report",
	}, "alice")
	if err != nil {
		t.Fatalf("CreateWakeup: %v", err)
	}
	if w.NextRunAt != clk.unix+600 {
		t.Fatalf("next_run_at = %d, want the requested instant %d", w.NextRunAt, clk.unix+600)
	}
	if w.Mode != jobstore.WakeupModeOnce || w.Enabled != 1 {
		t.Fatalf("wakeup = %+v, want an enabled once-mode timer", w)
	}
	if w.FilterJobID != src.ID {
		t.Fatalf("filter_job_id = %q, want the job it is registered on", w.FilterJobID)
	}
	if want := clk.unix + int64(config.DefaultWakeupTTLSec); w.ExpiresAt != want {
		t.Fatalf("expires_at = %d, want now + the 7-day default (%d)", w.ExpiresAt, want)
	}

	// Not due yet: the sweeper's query must not see it.
	due, err := s.Meta().DueWakeups(clk.unix)
	if err != nil {
		t.Fatalf("DueWakeups: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("due wakeups = %+v, want none before the instant", due)
	}

	clk.advance(601)
	due, err = s.Meta().DueWakeups(clk.unix)
	if err != nil {
		t.Fatalf("DueWakeups: %v", err)
	}
	if len(due) != 1 || due[0].ID != w.ID {
		t.Fatalf("due wakeups = %+v, want the timer that just came due", due)
	}
	// The sweeper advances before it fires (compare-and-swap); do the same here.
	if ok, err := s.Meta().AdvanceWakeup(w.ID, due[0].NextRunAt, 0, clk.unix); err != nil || !ok {
		t.Fatalf("AdvanceWakeup = %v/%v, want the fire to be won", ok, err)
	}
	s.FireWakeup(w.ID, jobstore.WakeupKindAt)

	fired := waitWakeupFired(t, s, w.ID, 1)
	if fired.Enabled != 0 {
		t.Fatalf("wakeup after a once fire = %+v, want it consumed (disabled)", fired)
	}
	cont, ok := s.Wait(fired.ContinuationJobID)
	if !ok {
		t.Fatalf("continuation job %s not found", fired.ContinuationJobID)
	}
	if cont.Status != StatusDone {
		t.Fatalf("continuation status = %s (err=%s), want done", cont.Status, cont.Error)
	}
	if cont.ResumedFrom != src.ID || cont.SessionID != src.SessionID {
		t.Fatalf("continuation = %+v, want it to continue %s's session", cont, src.ID)
	}
	if !contains(cont.Tags, wakeupTagPrefix+w.ID) {
		t.Fatalf("continuation tags = %v, want %s", cont.Tags, wakeupTagPrefix+w.ID)
	}

	detail := eventDetailOf(t, s, src.ID, EventJobWakeupFired)
	if detail["wakeup_id"] != w.ID || detail["continuation_job"] != cont.ID || detail["reason"] != jobstore.WakeupKindAt {
		t.Fatalf("job.wakeup_fired detail = %+v, want the wakeup, its continuation and the reason", detail)
	}

	// Re-enabling arms the timer again: the operator asked for it a second time.
	re, err := s.SetWakeupEnabled(w.ID, "alice", true)
	if err != nil {
		t.Fatalf("SetWakeupEnabled: %v", err)
	}
	if re.Enabled != 1 || re.NextRunAt != w.At {
		t.Fatalf("re-enabled wakeup = %+v, want enabled at its original instant", re)
	}
	s.FireWakeup(w.ID, jobstore.WakeupKindAt)
	second := waitWakeupFired(t, s, w.ID, 2)
	if second.ContinuationJobID == fired.ContinuationJobID {
		t.Fatalf("the second fire reused continuation %s, want a new job", second.ContinuationJobID)
	}
}

// TestWakeupEventOnOtherJobTerminal: an event subscription on another job fires only
// for the status it filtered on — the "wake me when the other job is done" case, and
// its negative (a failure does not wake the watcher that asked for done).
func TestWakeupEventOnOtherJobTerminal(t *testing.T) {
	root := t.TempDir()
	clk := &fakeClock{}
	s := newWakeupService(t, root, clk)

	src := finishedSessionJob(t, s, "codex", "prepare the branch")

	// B is submitted FIRST (async) so the wakeups can be registered against a job
	// that is still running — which is the only way the event is the trigger.
	other, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30, CallerID: "alice",
	})
	if err != nil {
		t.Fatalf("Submit other: %v", err)
	}
	onDone, err := s.CreateWakeup(WakeupSpec{
		JobID: src.ID, Kind: jobstore.WakeupKindEvent,
		EventTypes: []string{EventJobTerminal}, FilterJobID: other.ID,
		FilterStatus: []string{StatusDone}, Instruction: "merge the results",
	}, "alice")
	if err != nil {
		t.Fatalf("CreateWakeup(on done): %v", err)
	}
	onFailed, err := s.CreateWakeup(WakeupSpec{
		JobID: src.ID, Kind: jobstore.WakeupKindEvent,
		EventTypes: []string{EventJobTerminal}, FilterJobID: other.ID,
		FilterStatus: []string{StatusFailed}, Instruction: "investigate the failure",
	}, "alice")
	if err != nil {
		t.Fatalf("CreateWakeup(on failed): %v", err)
	}
	if onDone.Mode != jobstore.WakeupModeOnce {
		t.Fatalf("event wakeup mode = %q, want once by default", onDone.Mode)
	}

	final, ok := s.Wait(other.ID)
	if !ok || final.Status != StatusDone {
		t.Fatalf("other job = %+v (ok=%v), want done", final, ok)
	}
	fired := waitWakeupFired(t, s, onDone.ID, 1)
	cont, ok := s.Wait(fired.ContinuationJobID)
	if !ok || cont.ResumedFrom != src.ID {
		t.Fatalf("continuation = %+v (ok=%v), want a resume of %s", cont, ok, src.ID)
	}
	// The status filter is what separates the two subscriptions: the same event
	// (job.terminal) left the failed-only watcher alone.
	other2, ok2, err := s.GetWakeup(onFailed.ID)
	if err != nil {
		t.Fatalf("GetWakeup(on failed): %v", err)
	}
	if !ok2 || other2.FiredCount != 0 || other2.ContinuationJobID != "" {
		t.Fatalf("the failed-only watcher = %+v, want it untouched by a done terminal", other2)
	}
}

// TestWakeupDefaultsToOwnJobEvents: without --job-id an event wakeup watches the job
// it is registered on, which is what an agent registering from inside its own job
// means ("wake me when my own turn is answered").
func TestWakeupDefaultsToOwnJobEvents(t *testing.T) {
	root := t.TempDir()
	clk := &fakeClock{}
	s := newWakeupService(t, root, clk)

	src := finishedSessionJob(t, s, "codex", "ask the human something")
	w, err := s.CreateWakeup(WakeupSpec{
		JobID: src.ID, Kind: jobstore.WakeupKindEvent,
		EventTypes: []string{EventInteractionAnswered}, Instruction: "act on the answer",
	}, "alice")
	if err != nil {
		t.Fatalf("CreateWakeup: %v", err)
	}
	if w.FilterJobID != src.ID {
		t.Fatalf("filter_job_id = %q, want the registering job %s", w.FilterJobID, src.ID)
	}

	// An answer on ANOTHER job must not wake it.
	s.recordEvent("some-other-job", EventInteractionAnswered, map[string]any{"interaction_id": "i-1"})
	time.Sleep(50 * time.Millisecond)
	if cur, _, _ := s.GetWakeup(w.ID); cur.FiredCount != 0 {
		t.Fatalf("wakeup fired for another job's event: %+v", cur)
	}

	// The same event on its OWN job does.
	s.recordEvent(src.ID, EventInteractionAnswered, map[string]any{"interaction_id": "i-2", "answer": "go ahead"})
	fired := waitWakeupFired(t, s, w.ID, 1)
	cont, ok := s.Wait(fired.ContinuationJobID)
	if !ok || cont.ResumedFrom != src.ID {
		t.Fatalf("continuation = %+v (ok=%v), want a resume of %s", cont, ok, src.ID)
	}
}

// TestWakeupCoalescesWhileContinuationActive: one wakeup never stacks continuations
// (design §五 决策 6). A trigger that arrives while the previous continuation is still
// running only bumps coalesced_count — and the next trigger after it ends fires
// again, so a continuous timer keeps working across turns.
func TestWakeupCoalescesWhileContinuationActive(t *testing.T) {
	root := t.TempDir()
	clk := &fakeClock{}
	s := newWakeupService(t, root, clk)

	src := finishedSessionJob(t, s, "slow", "watch the queue")
	w, err := s.CreateWakeup(WakeupSpec{
		JobID: src.ID, Kind: jobstore.WakeupKindEvent,
		EventTypes: []string{EventJobStalled}, Mode: jobstore.WakeupModeContinuous,
		Instruction: "look at the queue again",
	}, "alice")
	if err != nil {
		t.Fatalf("CreateWakeup: %v", err)
	}

	s.FireWakeup(w.ID, EventJobStalled)
	first := waitWakeupFired(t, s, w.ID, 1)
	// The continuation sleeps, so it is still in flight for the second trigger.
	if cur, ok := s.Get(first.ContinuationJobID); ok && IsTerminal(cur.Status) {
		t.Fatalf("continuation %s already terminal (%s); the coalesce window is not being tested", cur.ID, cur.Status)
	}

	s.FireWakeup(w.ID, EventJobStalled)
	time.Sleep(50 * time.Millisecond)
	row, _, err := s.GetWakeup(w.ID)
	if err != nil {
		t.Fatalf("GetWakeup: %v", err)
	}
	if row.CoalescedCount != 1 || row.FiredCount != 1 {
		t.Fatalf("wakeup = %+v, want 1 coalesced and still only 1 fire", row)
	}
	if row.ContinuationJobID != first.ContinuationJobID {
		t.Fatalf("continuation changed to %s while %s was live", row.ContinuationJobID, first.ContinuationJobID)
	}
	detail := eventDetailOf(t, s, src.ID, EventJobWakeupCoalesced)
	if detail["wakeup_id"] != w.ID {
		t.Fatalf("job.wakeup_coalesced detail = %+v, want the wakeup id", detail)
	}

	// The continuous wakeup stayed armed, and once the continuation is over the next
	// trigger starts a NEW one (the slot is not sticky).
	if row.Enabled != 1 {
		t.Fatalf("wakeup = %+v, want a continuous wakeup still enabled", row)
	}
	if err := s.Cancel(first.ContinuationJobID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, ok := s.Wait(first.ContinuationJobID); !ok {
		t.Fatalf("continuation %s never finished", first.ContinuationJobID)
	}
	s.FireWakeup(w.ID, EventJobStalled)
	second := waitWakeupFired(t, s, w.ID, 2)
	if second.ContinuationJobID == first.ContinuationJobID {
		t.Fatalf("the fire after a terminal continuation reused %s", second.ContinuationJobID)
	}
}

// TestWakeupFallsBackToRebuildWithoutSession: a job that captured no session cannot
// be resumed, so a fire re-runs its original request with the instruction appended
// to the prompt (design §五 决策 6) — the original task text is kept, not replaced.
func TestWakeupFallsBackToRebuildWithoutSession(t *testing.T) {
	root := t.TempDir()
	clk := &fakeClock{}
	s := newWakeupService(t, root, clk)

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "plain", Runner: "local",
		Prompt: "original task text", Cwd: ".", TimeoutSec: 30, CallerID: "alice",
	})
	if src.SessionID != "" {
		t.Fatalf("source session_id = %q, want a job with no session to continue", src.SessionID)
	}
	w, err := s.CreateWakeup(WakeupSpec{
		JobID: src.ID, Kind: jobstore.WakeupKindAt, At: clk.unix + 60,
		Instruction: "check the CI result",
	}, "alice")
	if err != nil {
		t.Fatalf("CreateWakeup: %v", err)
	}
	clk.advance(61)
	s.FireWakeup(w.ID, jobstore.WakeupKindAt)

	fired := waitWakeupFired(t, s, w.ID, 1)
	cont, ok := s.Wait(fired.ContinuationJobID)
	if !ok {
		t.Fatalf("continuation job %s not found", fired.ContinuationJobID)
	}
	if cont.Status != StatusDone {
		t.Fatalf("continuation status = %s (err=%s), want done", cont.Status, cont.Error)
	}
	if cont.SourceJobID != src.ID || cont.ResumedFrom != "" {
		t.Fatalf("continuation = %+v, want a REBUILD of %s (source_job_id set, not a resume)", cont, src.ID)
	}
	got := promptFromRequestJSON(cont.RequestJSON)
	if !strings.Contains(got, "original task text") || !strings.Contains(got, wakeupPromptMarker+"check the CI result") {
		t.Fatalf("rebuilt prompt = %q, want the original text with the instruction appended", got)
	}
	if !contains(cont.Tags, wakeupTagPrefix+w.ID) {
		t.Fatalf("continuation tags = %v, want %s", cont.Tags, wakeupTagPrefix+w.ID)
	}
}

// TestWakeupEveryDoesNotReplayMissedTicks: an every-timer counts from the sweep
// instant, so a server that was down for a day fires ONCE when it comes back rather
// than replaying every tick it missed (design §五.1).
func TestWakeupEveryDoesNotReplayMissedTicks(t *testing.T) {
	root := t.TempDir()
	clk := &fakeClock{}
	s := newWakeupService(t, root, clk)

	src := finishedSessionJob(t, s, "codex", "poll the queue")
	w, err := s.CreateWakeup(WakeupSpec{
		JobID: src.ID, Kind: jobstore.WakeupKindEvery, EverySec: 3600,
		Instruction: "poll again",
	}, "alice")
	if err != nil {
		t.Fatalf("CreateWakeup: %v", err)
	}
	if w.NextRunAt != clk.unix+3600 {
		t.Fatalf("next_run_at = %d, want one interval from creation", w.NextRunAt)
	}
	if w.Mode != jobstore.WakeupModeContinuous {
		t.Fatalf("mode = %q, want continuous for an every timer", w.Mode)
	}

	// The server was down for ten hours: the tick that was missed is not replayed —
	// the next run is one interval after the SWEEP, not after the missed tick.
	clk.advance(10 * 3600)
	due, err := s.Meta().DueWakeups(clk.unix)
	if err != nil {
		t.Fatalf("DueWakeups: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("due wakeups = %+v, want exactly one (the missed ticks coalesce)", due)
	}
	next, err := WakeupNextRun(due[0], clk.unix)
	if err != nil {
		t.Fatalf("WakeupNextRun: %v", err)
	}
	if next != clk.unix+3600 {
		t.Fatalf("next run = %d, want %d (now + one interval, never the missed ticks)", next, clk.unix+3600)
	}
	if ok, err := s.Meta().AdvanceWakeup(w.ID, due[0].NextRunAt, next, clk.unix); err != nil || !ok {
		t.Fatalf("AdvanceWakeup = %v/%v, want the fire to be won", ok, err)
	}
	s.FireWakeup(w.ID, jobstore.WakeupKindEvery)
	fired := waitWakeupFired(t, s, w.ID, 1)
	if fired.FiredCount != 1 {
		t.Fatalf("fired_count = %d, want exactly one fire for the whole outage", fired.FiredCount)
	}
	row, _, err := s.GetWakeup(w.ID)
	if err != nil {
		t.Fatalf("GetWakeup: %v", err)
	}
	if row.Enabled != 1 || row.NextRunAt != next {
		t.Fatalf("wakeup after the fire = %+v, want it armed for %d", row, next)
	}
}

// TestWakeupExpires: a wakeup past its TTL is retired by the sweep (disabled +
// job.wakeup_expired) instead of firing, and a fire that gets there first does the
// same rather than starting work the sweeper is about to retire.
func TestWakeupExpires(t *testing.T) {
	root := t.TempDir()
	clk := &fakeClock{}
	s := newWakeupService(t, root, clk)

	src := finishedSessionJob(t, s, "codex", "keep an eye on things")
	w, err := s.CreateWakeup(WakeupSpec{
		JobID: src.ID, Kind: jobstore.WakeupKindAt, At: clk.unix + 600,
		ExpiresAt: clk.unix + 60, Instruction: "report",
	}, "alice")
	if err != nil {
		t.Fatalf("CreateWakeup: %v", err)
	}
	clk.advance(120)

	due, err := s.Meta().DueWakeups(clk.unix)
	if err != nil {
		t.Fatalf("DueWakeups: %v", err)
	}
	if len(due) != 1 || due[0].ID != w.ID {
		t.Fatalf("due wakeups = %+v, want the expired row (the sweep retires it)", due)
	}
	s.ExpireWakeup(w.ID)
	row, _, err := s.GetWakeup(w.ID)
	if err != nil {
		t.Fatalf("GetWakeup: %v", err)
	}
	if row.Enabled != 0 {
		t.Fatalf("expired wakeup = %+v, want it disabled", row)
	}
	detail := eventDetailOf(t, s, src.ID, EventJobWakeupExpired)
	if detail["wakeup_id"] != w.ID {
		t.Fatalf("job.wakeup_expired detail = %+v, want the wakeup id", detail)
	}

	// A fire on the retired wakeup starts nothing.
	s.FireWakeup(w.ID, jobstore.WakeupKindAt)
	time.Sleep(50 * time.Millisecond)
	if row, _, _ := s.GetWakeup(w.ID); row.FiredCount != 0 || row.ContinuationJobID != "" {
		t.Fatalf("expired wakeup fired anyway: %+v", row)
	}

	// The same holds when the fire arrives first: it retires the row instead of
	// resuming (a stale timer must not start work after its TTL).
	if err := s.Meta().SetWakeupEnabled(w.ID, 1); err != nil {
		t.Fatalf("SetWakeupEnabled: %v", err)
	}
	s.FireWakeup(w.ID, jobstore.WakeupKindAt)
	row, _, err = s.GetWakeup(w.ID)
	if err != nil {
		t.Fatalf("GetWakeup: %v", err)
	}
	if row.Enabled != 0 || row.FiredCount != 0 {
		t.Fatalf("wakeup = %+v, want the fire to retire it without starting a continuation", row)
	}
}

// TestWakeupContinuationDoesNotRetrigger: the events of the job a wakeup STARTED must
// not feed back into that same wakeup (design §五.1). The control in the same test —
// an identical subscription whose slot is empty — fires, so the assertion is about
// the guard and not about matching.
func TestWakeupContinuationDoesNotRetrigger(t *testing.T) {
	root := t.TempDir()
	clk := &fakeClock{}
	s := newWakeupService(t, root, clk)

	src := finishedSessionJob(t, s, "codex", "supervise the worker")
	watched := finishedSessionJob(t, s, "codex", "the job being watched")

	// W watches `watched`; its continuation slot is held by a job with THAT id, which
	// is exactly the "my own continuation produced this event" situation.
	w, err := s.CreateWakeup(WakeupSpec{
		JobID: src.ID, Kind: jobstore.WakeupKindEvent,
		EventTypes: []string{EventJobTerminal}, FilterJobID: watched.ID,
		Mode: jobstore.WakeupModeContinuous, Instruction: "react",
	}, "alice")
	if err != nil {
		t.Fatalf("CreateWakeup: %v", err)
	}
	if ok, err := s.Meta().ClaimWakeupFire(w.ID, watched.ID); err != nil || !ok {
		t.Fatalf("ClaimWakeupFire = %v/%v, want the slot taken", ok, err)
	}
	before, _, _ := s.GetWakeup(w.ID)
	s.recordEvent(watched.ID, EventJobTerminal, map[string]any{"status": StatusDone})
	time.Sleep(50 * time.Millisecond)
	after, _, err := s.GetWakeup(w.ID)
	if err != nil {
		t.Fatalf("GetWakeup: %v", err)
	}
	if after.FiredCount != before.FiredCount || after.ContinuationJobID != watched.ID {
		t.Fatalf("wakeup = %+v, want no second fire from its own continuation's event", after)
	}

	// Control: the same subscription with a free slot DOES fire on that event.
	control, err := s.CreateWakeup(WakeupSpec{
		JobID: src.ID, Kind: jobstore.WakeupKindEvent,
		EventTypes: []string{EventJobTerminal}, FilterJobID: watched.ID,
		Instruction: "react",
	}, "alice")
	if err != nil {
		t.Fatalf("CreateWakeup(control): %v", err)
	}
	s.recordEvent(watched.ID, EventJobTerminal, map[string]any{"status": StatusDone})
	waitWakeupFired(t, s, control.ID, 1)
}

// TestWakeupCreateRequiresResumePermission: registering (or switching) a wakeup is
// the same privilege as resuming the job — its own caller, or a caller holding the
// answer capability when governance enforces it (design §五.1 权限).
func TestWakeupCreateRequiresResumePermission(t *testing.T) {
	root := t.TempDir()
	clk := &fakeClock{}
	gateOn := true
	cfg := &config.Config{
		Server: config.ServerConfig{
			Governance: config.GovernanceConfig{RequireAnswerCapability: gateOn},
			Callers: []config.CallerConfig{
				{ID: "alice", CanAnswer: true},
				{ID: "bob"},
			},
		},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath: root, AllowedAgents: []string{"exec"},
				AllowedRunners: []string{"local"}, AllowExec: true,
			},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	clk.unix = 1_800_000_000
	s.nowFn = clk.now

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30, CallerID: "alice",
	})
	spec := WakeupSpec{JobID: src.ID, Kind: jobstore.WakeupKindAt, At: clk.unix + 600, Instruction: "again"}

	// bob owns neither the job nor the capability.
	if _, err := s.CreateWakeup(spec, "bob"); !errors.Is(err, ErrWakeupForbidden) {
		t.Fatalf("CreateWakeup(bob) error = %v, want ErrWakeupForbidden", err)
	}
	// The job's own caller may, and so may a capability holder.
	w, err := s.CreateWakeup(spec, "alice")
	if err != nil {
		t.Fatalf("CreateWakeup(alice): %v", err)
	}
	if _, err := s.CreateWakeup(WakeupSpec{JobID: src.ID, Kind: jobstore.WakeupKindAt, At: clk.unix + 900}, "alice"); err != nil {
		t.Fatalf("CreateWakeup(owner, no instruction): %v", err)
	}
	if _, err := s.SetWakeupEnabled(w.ID, "bob", false); !errors.Is(err, ErrWakeupForbidden) {
		t.Fatalf("SetWakeupEnabled(bob) error = %v, want ErrWakeupForbidden", err)
	}
	if err := s.DeleteWakeup(w.ID, "bob"); !errors.Is(err, ErrWakeupForbidden) {
		t.Fatalf("DeleteWakeup(bob) error = %v, want ErrWakeupForbidden", err)
	}

	// With the gate OFF (the default), any authenticated caller may — the capability
	// only matters when governance enforces it.
	cfg2 := *cfg
	cfg2.Server.Governance.RequireAnswerCapability = false
	s.Reload(&cfg2)
	if _, err := s.CreateWakeup(WakeupSpec{JobID: src.ID, Kind: jobstore.WakeupKindAt, At: clk.unix + 1200}, "bob"); err != nil {
		t.Fatalf("CreateWakeup(bob) with the gate off: %v", err)
	}
}

// TestWakeupCreateRejectsBadSpecs: the validation that keeps a wakeup from being
// registered in a state it can never fire from (a past instant, a sub-minute timer,
// an unparseable cron, an unknown event type, a status filter on a non-terminal
// event) — each is a 400-class ErrInvalidRequest.
func TestWakeupCreateRejectsBadSpecs(t *testing.T) {
	root := t.TempDir()
	clk := &fakeClock{}
	s := newWakeupService(t, root, clk)
	src := finishedSessionJob(t, s, "codex", "x")

	cases := []struct {
		name string
		spec WakeupSpec
	}{
		{"unknown kind", WakeupSpec{Kind: "later"}},
		{"at in the past", WakeupSpec{Kind: jobstore.WakeupKindAt, At: clk.unix - 1}},
		{"every below a minute", WakeupSpec{Kind: jobstore.WakeupKindEvery, EverySec: 30}},
		{"bad cron", WakeupSpec{Kind: jobstore.WakeupKindCron, Cron: "not a cron"}},
		{"unknown timezone", WakeupSpec{Kind: jobstore.WakeupKindCron, Cron: "* * * * *", Timezone: "Mars/Olympus"}},
		{"event with no type", WakeupSpec{Kind: jobstore.WakeupKindEvent}},
		{"unknown event type", WakeupSpec{Kind: jobstore.WakeupKindEvent, EventTypes: []string{"job.exploded"}}},
		{"status on a non-terminal event", WakeupSpec{
			Kind: jobstore.WakeupKindEvent, EventTypes: []string{EventInteractionAnswered},
			FilterStatus: []string{StatusDone},
		}},
		{"unknown status", WakeupSpec{
			Kind: jobstore.WakeupKindEvent, EventTypes: []string{EventJobTerminal},
			FilterStatus: []string{"nearly"},
		}},
		{"unknown mode", WakeupSpec{Kind: jobstore.WakeupKindAt, At: clk.unix + 60, Mode: "sometimes"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.JobID = src.ID
			if _, err := s.CreateWakeup(tc.spec, "alice"); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("CreateWakeup(%s) error = %v, want ErrInvalidRequest", tc.name, err)
			}
		})
	}

	// An unknown target job is the 404 case, not a 400.
	if _, err := s.CreateWakeup(WakeupSpec{JobID: "nope", Kind: jobstore.WakeupKindAt, At: clk.unix + 60}, "alice"); !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("CreateWakeup(unknown job) error = %v, want ErrUnknownJob", err)
	}
	// And the accepted shapes really are accepted (the table above must not be
	// passing for the wrong reason).
	if _, err := s.CreateWakeup(WakeupSpec{
		JobID: src.ID, Kind: jobstore.WakeupKindCron, Cron: "0 9 * * 1-5", Timezone: "UTC",
	}, "alice"); err != nil {
		t.Fatalf("CreateWakeup(cron): %v", err)
	}
	w, err := s.CreateWakeup(WakeupSpec{
		JobID: src.ID, Kind: jobstore.WakeupKindEvent,
		EventTypes: []string{EventJobTerminal}, FilterStatus: []string{StatusDone, StatusFailed},
	}, "alice")
	if err != nil {
		t.Fatalf("CreateWakeup(status filter): %v", err)
	}
	if got := jobstore.DecodeStringList(w.FilterStatusJSON); len(got) != 2 {
		t.Fatalf("filter_status = %v, want both statuses stored", got)
	}
	if !wakeupStatusMatches(w, EventJobTerminal, map[string]any{"status": StatusFailed}) {
		t.Fatalf("status filter did not match a listed status")
	}
	if wakeupStatusMatches(w, EventJobTerminal, map[string]any{"status": StatusCancelled}) {
		t.Fatalf("status filter matched an unlisted status")
	}
}
