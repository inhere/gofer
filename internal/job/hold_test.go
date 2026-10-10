package job

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// holdConfig is one project ("self") that allows exec on the local runner.
func holdConfig(root string, mutate func(*config.Config)) *config.Config {
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	if mutate != nil {
		mutate(cfg)
	}
	return cfg
}

func newHoldService(t *testing.T, root string, mutate func(*config.Config)) *Service {
	t.Helper()
	return newServiceFromCfg(t, root, holdConfig(root, mutate))
}

// probeRequest is a held exec job whose only effect is creating probe — the evidence
// of whether the job ran at all.
func probeRequest(bin, probe string) JobRequest {
	return JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{bin, "write-files", probe, "ran"}, Cwd: ".", TimeoutSec: 30,
		Hold: true, HoldReason: "push the release branch",
	}
}

func assertNoProbe(t *testing.T, probe string) {
	t.Helper()
	if _, err := os.Stat(probe); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe %s exists (err=%v): the held job ran without approval", probe, err)
	}
}

func countEvents(types []string, want string) int {
	n := 0
	for _, ty := range types {
		if ty == want {
			n++
		}
	}
	return n
}

// mustHold submits a held request and checks it parked: awaiting_approval, no
// in-memory entry, nothing run.
func mustHold(t *testing.T, s *Service, req JobRequest, probe string) JobResult {
	t.Helper()
	res, err := s.Submit(req)
	if err != nil {
		t.Fatalf("Submit(hold): %v", err)
	}
	if res.Status != StatusAwaitingApproval {
		t.Fatalf("status = %s, want %s", res.Status, StatusAwaitingApproval)
	}
	if s.entry(res.ID) != nil {
		t.Fatalf("a held job must have no in-memory entry")
	}
	if probe != "" {
		assertNoProbe(t, probe)
	}
	return res
}

// TestHoldApproveRunsUnderSameID: a held exec job does not run; approving it runs it
// under the same id to done, with one job.submitted and the hold events in order.
func TestHoldApproveRunsUnderSameID(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := newHoldService(t, root, nil)
	probe := filepath.Join(root, "probe.txt")

	req := probeRequest(bin, probe)
	req.RequestID = "req-hold-1" // the re-entry must not find itself through it
	held := mustHold(t, s, req, probe)

	got, ok := s.Get(held.ID)
	if !ok || got.Status != StatusAwaitingApproval {
		t.Fatalf("Get = %+v, %v", got.Status, ok)
	}
	if got.Hold == nil || got.Hold.Reason != "push the release branch" || got.Hold.ExpiresAt <= got.StartedAt {
		t.Fatalf("hold state = %+v", got.Hold)
	}
	if got.Hold.TimeoutSec != config.DefaultHoldTimeoutSec {
		t.Fatalf("hold timeout = %d, want the default", got.Hold.TimeoutSec)
	}
	if len(got.Hold.Command) == 0 || got.Hold.Command[1] != "write-files" {
		t.Fatalf("hold command = %v", got.Hold.Command)
	}
	if got.Title == "" {
		t.Fatalf("a held job keeps its title")
	}
	// The digest is server-only: it never appears in the API body.
	body, _ := json.Marshal(got)
	if strings.Contains(string(body), "digest") || strings.Contains(string(body), "carry") {
		t.Fatalf("API body leaks the hold secret: %s", body)
	}
	// A duplicate submit with the same request_id lands on the held job.
	dup, err := s.Submit(req)
	if err != nil || dup.ID != held.ID {
		t.Fatalf("duplicate held submit = %s, %v; want %s", dup.ID, err, held.ID)
	}

	res, err := s.ApproveJob(held.ID, "alice", "go")
	if err != nil {
		t.Fatalf("ApproveJob: %v", err)
	}
	if res.ID != held.ID {
		t.Fatalf("approved job id = %s, want %s", res.ID, held.ID)
	}
	final, _ := s.Wait(held.ID)
	if final.Status != StatusDone {
		t.Fatalf("final = %s (err=%s)", final.Status, final.Error)
	}
	if b, err := os.ReadFile(probe); err != nil || string(b) != "ran" {
		t.Fatalf("probe = %q, %v", b, err)
	}
	if final.Hold == nil || final.Hold.Decision != HoldDecisionApproved || final.Hold.DecidedBy != "alice" || final.Hold.Note != "go" {
		t.Fatalf("decision not kept on the row: %+v", final.Hold)
	}
	// hold follows the request: the approved run's request_json still says hold.
	if h, _, _ := holdFromRequest(final.RequestJSON); !h {
		t.Fatalf("request_json lost hold=true: %s", final.RequestJSON)
	}

	types := eventTypes(t, s, held.ID)
	if n := countEvents(types, EventJobSubmitted); n != 1 {
		t.Fatalf("job.submitted recorded %d times: %v", n, types)
	}
	if !hasSubsequence(types, []string{EventJobSubmitted, EventJobAwaitingApproval, EventJobHoldApproved, EventJobRunning, EventJobTerminal}) {
		t.Fatalf("event order: %v", types)
	}
}

// TestHoldRejectCancelsWithoutRunning: reject → cancelled, nothing ran, the reject
// and terminal events are recorded; --resume is refused; a note is optional.
func TestHoldRejectCancelsWithoutRunning(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := newHoldService(t, root, nil)
	probe := filepath.Join(root, "probe.txt")
	held := mustHold(t, s, probeRequest(bin, probe), probe)

	if _, err := s.RejectJob(held.ID, "alice", "no", true); !errors.Is(err, ErrHoldResume) || !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("reject --resume err = %v, want ErrHoldResume", err)
	}
	out, err := s.RejectJob(held.ID, "alice", "not today", false)
	if err != nil {
		t.Fatalf("RejectJob: %v", err)
	}
	if out.Status != StatusCancelled || out.Error != "hold rejected by alice: not today" {
		t.Fatalf("rejected = %s / %q", out.Status, out.Error)
	}
	if out.Hold == nil || out.Hold.Decision != HoldDecisionRejected || out.EndedAt == 0 {
		t.Fatalf("decision = %+v ended_at=%d", out.Hold, out.EndedAt)
	}
	assertNoProbe(t, probe)
	types := eventTypes(t, s, held.ID)
	if !hasSubsequence(types, []string{EventJobAwaitingApproval, EventJobHoldRejected, EventJobTerminal}) {
		t.Fatalf("event order: %v", types)
	}
	if _, err := s.ApproveJob(held.ID, "bob", ""); !errors.Is(err, ErrJobNotAwaitingApproval) {
		t.Fatalf("approve after reject err = %v", err)
	}

	// No note: still a rejection, recorded with the anonymous author.
	other := mustHold(t, s, probeRequest(bin, probe), probe)
	out, err = s.RejectJob(other.ID, "", "", false)
	if err != nil || out.Error != "hold rejected by anonymous" {
		t.Fatalf("note-less reject = %q, %v", out.Error, err)
	}
}

// TestHoldApproveTwice: the second approval loses — the job was already decided.
func TestHoldApproveTwice(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := newHoldService(t, root, nil)
	probe := filepath.Join(root, "probe.txt")
	held := mustHold(t, s, probeRequest(bin, probe), probe)

	if _, err := s.ApproveJob(held.ID, "alice", ""); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	if _, err := s.ApproveJob(held.ID, "bob", ""); !errors.Is(err, ErrJobNotAwaitingApproval) {
		t.Fatalf("second approve err = %v, want ErrJobNotAwaitingApproval", err)
	}
	final, _ := s.Wait(held.ID)
	if final.Status != StatusDone {
		t.Fatalf("final = %s (%s)", final.Status, final.Error)
	}
	if n := countEvents(eventTypes(t, s, held.ID), EventJobHoldApproved); n != 1 {
		t.Fatalf("job.hold_approved recorded %d times", n)
	}
}

// TestHoldSurvivesRestart: a held job is database state only — a new Service over the
// same store (a restart) plus the orphan reconcile leaves it waiting, and approving it
// there runs it.
func TestHoldSurvivesRestart(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	first := newHoldService(t, root, nil)
	probe := filepath.Join(root, "probe.txt")
	held := mustHold(t, first, probeRequest(bin, probe), probe)

	second := newHoldService(t, root, nil)
	if _, err := second.ReconcileOrphanJobs(); err != nil {
		t.Fatalf("ReconcileOrphanJobs: %v", err)
	}
	got, ok := second.Get(held.ID)
	if !ok || got.Status != StatusAwaitingApproval {
		t.Fatalf("after restart: %s, %v", got.Status, ok)
	}
	assertNoProbe(t, probe)
	if _, err := second.ApproveJob(held.ID, "alice", ""); err != nil {
		t.Fatalf("approve after restart: %v", err)
	}
	final, _ := second.Wait(held.ID)
	if final.Status != StatusDone {
		t.Fatalf("final = %s (%s)", final.Status, final.Error)
	}
	if _, err := os.Stat(probe); err != nil {
		t.Fatalf("probe missing after approved run: %v", err)
	}
}

// TestHoldFollowsTheRequest: rebuilding an approved held job is held again — an
// approval covers one run.
func TestHoldFollowsTheRequest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := newHoldService(t, root, nil)
	probe := filepath.Join(root, "probe.txt")
	held := mustHold(t, s, probeRequest(bin, probe), probe)
	if _, err := s.ApproveJob(held.ID, "alice", ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if final, _ := s.Wait(held.ID); final.Status != StatusDone {
		t.Fatalf("final = %s", final.Status)
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}

	again, err := s.RebuildJob(held.ID, RebuildOverrides{}, "", "")
	if err != nil {
		t.Fatalf("RebuildJob: %v", err)
	}
	if again.Status != StatusAwaitingApproval || again.ID == held.ID {
		t.Fatalf("rebuild = %s (%s), want a NEW held job", again.ID, again.Status)
	}
	if again.SourceJobID != held.ID {
		t.Fatalf("rebuild lineage = %q", again.SourceJobID)
	}
	assertNoProbe(t, probe)

	// The lineage marker (json:"-") rides the hold record into the approved run.
	if _, err := s.ApproveJob(again.ID, "alice", ""); err != nil {
		t.Fatalf("approve rebuild: %v", err)
	}
	final, _ := s.Wait(again.ID)
	if final.Status != StatusDone || final.SourceJobID != held.ID {
		t.Fatalf("rebuilt run = %s source=%q", final.Status, final.SourceJobID)
	}
}

// TestHoldTakesNoConcurrencySlot: with a project cap of 1, a held job does not block
// the next job.
func TestHoldTakesNoConcurrencySlot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := newHoldService(t, root, func(c *config.Config) {
		p := c.Projects["self"]
		p.MaxConcurrentJobs = 1
		c.Projects["self"] = p
	})
	mustHold(t, s, probeRequest(bin, filepath.Join(root, "held.txt")), "")
	mustHold(t, s, probeRequest(bin, filepath.Join(root, "held2.txt")), "")

	plain := probeRequest(bin, filepath.Join(root, "plain.txt"))
	plain.Hold = false
	res, err := s.Submit(plain)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final, ok := s.WaitFor(res.ID, wait.Timeout(t, 10*time.Second))
	if !ok || final.Status != StatusDone {
		t.Fatalf("plain job behind two held ones = %s, finished=%v", final.Status, ok)
	}
}

// TestHoldDriftRefused: a config change that alters what the approved request would
// run (here the role's env) makes the approval fail with ErrHoldDrift; nothing runs.
func TestHoldDriftRefused(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	withRole := func(v string) func(*config.Config) {
		return func(c *config.Config) {
			c.Roles = map[string]config.RoleConfig{"pusher": {Env: map[string]string{"TARGET": v}}}
		}
	}
	s := newHoldService(t, root, withRole("main"))
	probe := filepath.Join(root, "probe.txt")
	req := probeRequest(bin, probe)
	req.Role = "pusher"
	held := mustHold(t, s, req, probe)

	s.Reload(holdConfig(root, withRole("release")))
	_, err := s.ApproveJob(held.ID, "alice", "")
	if !errors.Is(err, ErrHoldDrift) {
		t.Fatalf("approve after drift err = %v, want ErrHoldDrift", err)
	}
	got, _ := s.Get(held.ID)
	if got.Status != StatusFailed || !strings.Contains(got.Error, "approved but could not start") {
		t.Fatalf("drifted job = %s / %q", got.Status, got.Error)
	}
	assertNoProbe(t, probe)
	if !hasSubsequence(eventTypes(t, s, held.ID), []string{EventJobHoldApproved, EventJobTerminal}) {
		t.Fatalf("events: %v", eventTypes(t, s, held.ID))
	}
}

// TestHoldExpirySweep: with an injected clock, the sweep leaves a hold alone before
// its deadline and cancels it (job.hold_expired + job.terminal) at the deadline.
func TestHoldExpirySweep(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := newHoldService(t, root, nil)
	t0 := time.Unix(1_800_000_000, 0)
	s.nowFn = func() time.Time { return t0 }
	probe := filepath.Join(root, "probe.txt")
	req := probeRequest(bin, probe)
	req.HoldTimeoutSec = 60
	held := mustHold(t, s, req, probe)

	if n, err := s.SweepExpiredHolds(t0.Unix() + 59); err != nil || n != 0 {
		t.Fatalf("early sweep = %d, %v", n, err)
	}
	if n, err := s.SweepExpiredHolds(t0.Unix() + 60); err != nil || n != 1 {
		t.Fatalf("due sweep = %d, %v", n, err)
	}
	got, _ := s.Get(held.ID)
	if got.Status != StatusCancelled || got.Error != "hold expired" || got.Hold.Decision != HoldDecisionExpired {
		t.Fatalf("expired = %s / %q / %+v", got.Status, got.Error, got.Hold)
	}
	if !hasSubsequence(eventTypes(t, s, held.ID), []string{EventJobAwaitingApproval, EventJobHoldExpired, EventJobTerminal}) {
		t.Fatalf("events: %v", eventTypes(t, s, held.ID))
	}
	if n, _ := s.SweepExpiredHolds(t0.Unix() + 600); n != 0 {
		t.Fatalf("a decided hold was expired again")
	}
	assertNoProbe(t, probe)
}

// TestHoldSweepRacesApprove: an approval and the expiry sweep on the same due job —
// exactly one of them wins, and the job's end state agrees with the winner.
func TestHoldSweepRacesApprove(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := newHoldService(t, root, nil)
	t0 := time.Unix(1_800_000_000, 0)
	s.nowFn = func() time.Time { return t0 }
	for i := 0; i < 4; i++ {
		probe := filepath.Join(root, "probe-"+string(rune('a'+i))+".txt")
		req := probeRequest(bin, probe)
		req.HoldTimeoutSec = 60
		held := mustHold(t, s, req, probe)

		var wg sync.WaitGroup
		var approveErr, sweepErr error
		var expired int
		wg.Add(2)
		go func() { defer wg.Done(); _, approveErr = s.ApproveJob(held.ID, "alice", "") }()
		go func() { defer wg.Done(); expired, sweepErr = s.SweepExpiredHolds(t0.Unix() + 120) }()
		wg.Wait()
		if sweepErr != nil {
			t.Fatalf("sweep: %v", sweepErr)
		}
		approved := approveErr == nil
		if approved == (expired == 1) {
			t.Fatalf("round %d: approved=%v (err=%v) expired=%d — want exactly one winner", i, approved, approveErr, expired)
		}
		final, _ := s.Wait(held.ID)
		if approved && final.Status != StatusDone {
			t.Fatalf("round %d: approval won but job is %s", i, final.Status)
		}
		if !approved {
			if !errors.Is(approveErr, ErrJobNotAwaitingApproval) || final.Status != StatusCancelled {
				t.Fatalf("round %d: sweep won but approve err=%v status=%s", i, approveErr, final.Status)
			}
			assertNoProbe(t, probe)
		}
	}
}

// TestHoldCancel: cancelling a held job withdraws it without running it.
func TestHoldCancel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := newHoldService(t, root, nil)
	probe := filepath.Join(root, "probe.txt")
	held := mustHold(t, s, probeRequest(bin, probe), probe)
	if err := s.Cancel(held.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got, _ := s.Get(held.ID)
	if got.Status != StatusCancelled || got.Hold.Decision != HoldDecisionCancelled {
		t.Fatalf("cancelled = %s / %+v", got.Status, got.Hold)
	}
	if !hasSubsequence(eventTypes(t, s, held.ID), []string{EventJobAwaitingApproval, EventJobCancelled, EventJobTerminal}) {
		t.Fatalf("events: %v", eventTypes(t, s, held.ID))
	}
	if err := s.Cancel(held.ID); err != nil {
		t.Fatalf("second cancel must be a no-op: %v", err)
	}
	assertNoProbe(t, probe)
}

// TestHoldSubmitSyncIsAsync: a synchronous submit of a held job returns at once,
// flagged async — it waits for a human, not for a process.
func TestHoldSubmitSyncIsAsync(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newHoldService(t, root, nil)
	res, async, err := s.SubmitSync(probeRequest(testcmd.Path(t), filepath.Join(root, "p.txt")), true)
	if err != nil || !async || res.Status != StatusAwaitingApproval {
		t.Fatalf("SubmitSync = %s async=%v err=%v", res.Status, async, err)
	}
}

// TestHoldRefusedCombinations: the shapes v1 cannot hold are refused up front with a
// reason, and a bad hold timeout is refused rather than truncated.
func TestHoldRefusedCombinations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := newHoldService(t, root, func(c *config.Config) {
		p := c.Projects["self"]
		allow := true
		p.AllowInteractive = &allow
		c.Projects["self"] = p
		c.Server.Hold = config.HoldConfig{MaxTimeoutSec: 3600, DefaultTimeoutSec: 600}
	})
	base := probeRequest(bin, filepath.Join(root, "p.txt"))
	cases := []struct {
		name   string
		mutate func(*JobRequest)
		want   string
	}{
		{"session", func(r *JobRequest) { r.Session = true }, "--session"},
		{"interactive", func(r *JobRequest) { r.Interactive = true }, "--interactive"},
		{"workflow step", func(r *JobRequest) { r.WorkflowID = "wf-1" }, "workflow"},
		{"negative timeout", func(r *JobRequest) { r.HoldTimeoutSec = -1 }, ">= 0"},
		{"timeout above the ceiling", func(r *JobRequest) { r.HoldTimeoutSec = 3601 }, "exceeds the maximum"},
	}
	for _, tc := range cases {
		req := base
		tc.mutate(&req)
		_, err := s.Submit(req)
		if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest mentioning %q", tc.name, err, tc.want)
		}
	}
	list, err := s.meta.ListJobs(jobstore.ListQuery{Project: "self"})
	if err != nil || len(list) != 0 {
		t.Fatalf("refused holds created rows: %d, %v", len(list), err)
	}
}
