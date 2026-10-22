package acp

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	acpproto "github.com/inhere/gofer/internal/acp"
	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// eventLog collects the job events a runner raises, so a test can assert on the timeline.
type eventLog struct {
	mu     sync.Mutex
	types  []string
	detail map[string][]map[string]any
}

func (l *eventLog) record(eventType string, detail map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.types = append(l.types, eventType)
	if l.detail == nil {
		l.detail = map[string][]map[string]any{}
	}
	l.detail[eventType] = append(l.detail[eventType], detail)
}

func (l *eventLog) first(eventType string) map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	if d := l.detail[eventType]; len(d) > 0 {
		return d[0]
	}
	return nil
}

// waitingSink stands in for the human: it holds the approval for delay and then answers
// allow_once. It deliberately ignores ctx — the point of these tests is that the ANSWER is
// slow, not that the job ends.
type waitingSink struct {
	delay time.Duration
	asked chan struct{}
}

func (s *waitingSink) RequestApproval(_ context.Context, _ runner.ApprovalRequest) (runner.ApprovalOutcome, error) {
	if s.asked != nil {
		close(s.asked)
	}
	time.Sleep(s.delay)
	return runner.ApprovalOutcome{Answer: acptest.AllowOnceOptionID, By: "human"}, nil
}

// expiredSink is a human who never answers: it blocks until the approval's own deadline
// and reports it, which is what the job's interaction surface does.
type expiredSink struct{}

func (expiredSink) RequestApproval(ctx context.Context, _ runner.ApprovalRequest) (runner.ApprovalOutcome, error) {
	<-ctx.Done()
	return runner.ApprovalOutcome{}, ctx.Err()
}

// residentRequest builds an acp-agent job that runs as a RESIDENT session (so a turn has
// a budget) with the given approval policy, fake-agent options and approval sink.
func residentRequest(t *testing.T, opts acptest.Options, approval config.ApprovalConfig, sink runner.ApprovalSink, turnTimeoutSec, idleTimeoutSec int, log *eventLog) runner.Request {
	t.Helper()
	return runner.Request{
		JobID:     "job-turn-clock",
		Command:   testcmd.Path(t),
		Args:      acptest.CmdArgs(opts),
		WorkDir:   t.TempDir(),
		Stdout:    io.Discard,
		Stderr:    io.Discard,
		Approvals: sink,
		OnJobEvent: func(eventType string, detail map[string]any) {
			log.record(eventType, detail)
		},
		ACP: &runner.ACPRequest{
			Prompt:          "hello",
			ResultDir:       t.TempDir(),
			SessionCommands: make(chan runner.SessionCommand), // non-nil = resident
			Approval:        approval,
			TurnTimeoutSec:  turnTimeoutSec,
			IdleTimeoutSec:  idleTimeoutSec,
		},
	}
}

// TestTurnDoesNotTimeOutWhileApprovalPending is gofer-ji7m: the turn's budget is 1s, a
// human takes several seconds to approve, and the turn still ends normally — the wait is
// not charged to the agent. The timeline says the clock was paused and resumed.
func TestTurnDoesNotTimeOutWhileApprovalPending(t *testing.T) {
	sink := &waitingSink{delay: 2200 * time.Millisecond, asked: make(chan struct{})}
	log := &eventLog{}
	req := residentRequest(t,
		acptest.Options{PermissionKind: acpproto.ToolKindEdit},
		config.ApprovalConfig{Mode: config.ApprovalAsk, TimeoutSec: 600},
		sink, 1, 1, log)

	res := (&Runner{}).Run(t.Context(), req)
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("run failed after a %.1fs approval wait on a 1s turn budget: exit=%d err=%v",
			2.2, res.ExitCode, res.Err)
	}
	select {
	case <-sink.asked:
	default:
		t.Fatal("the approval gate never asked a human, so the pause was not exercised")
	}
	paused := log.first(runner.EventTurnTimeoutPaused)
	if paused == nil {
		t.Fatalf("no %s event recorded (events: %v)", runner.EventTurnTimeoutPaused, log.types)
	}
	if paused["reason"] != "approval" || paused["timeout_sec"] != 1 {
		t.Fatalf("pause detail = %+v, want reason=approval timeout_sec=1", paused)
	}
	for _, key := range []string{"used_sec", "remaining_sec"} {
		if _, ok := paused[key]; !ok {
			t.Fatalf("pause detail %+v has no %q", paused, key)
		}
	}
	resumed := log.first(runner.EventTurnTimeoutResumed)
	if resumed == nil {
		t.Fatalf("no %s event recorded (events: %v)", runner.EventTurnTimeoutResumed, log.types)
	}
	if remained, _ := resumed["remaining_sec"].(int); remained < 0 || remained > 1 {
		t.Fatalf("resume detail = %+v, want a remaining_sec within the 1s budget", resumed)
	}
}

// TestTurnStillTimesOutWithoutApproval: the pause is specific to waiting on a human. An
// agent that wedges mid-turn (no permission request) still hits the turn budget.
func TestTurnStillTimesOutWithoutApproval(t *testing.T) {
	log := &eventLog{}
	req := residentRequest(t,
		acptest.Options{Hang: true},
		config.ApprovalConfig{Mode: config.ApprovalAsk, TimeoutSec: 600},
		&waitingSink{}, 1, 1, log)

	res := (&Runner{}).Run(t.Context(), req)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "turn timed out after 1s") {
		t.Fatalf("run = exit %d err %v, want the turn timeout", res.ExitCode, res.Err)
	}
	if log.first(runner.EventTurnTimeoutPaused) != nil {
		t.Fatalf("%s recorded for a turn nobody was asked about", runner.EventTurnTimeoutPaused)
	}
}

// TestApprovalOwnTimeoutStillApplies: pausing the turn clock does not extend the approval's
// own deadline — an answer nobody gives is resolved by on_timeout (allow here), the agent
// is told allow_once, and the turn finishes.
func TestApprovalOwnTimeoutStillApplies(t *testing.T) {
	log := &eventLog{}
	req := residentRequest(t,
		acptest.Options{PermissionKind: acpproto.ToolKindEdit},
		config.ApprovalConfig{Mode: config.ApprovalAsk, TimeoutSec: 1, OnTimeout: config.ApprovalOnTimeoutAllow},
		expiredSink{}, 30, 1, log)

	res := (&Runner{}).Run(t.Context(), req)
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("run = exit %d err %v, want the agent answered by on_timeout=allow", res.ExitCode, res.Err)
	}
	timedOut := log.first(runner.EventPermissionTimedOut)
	if timedOut == nil {
		t.Fatalf("no %s event recorded (events: %v)", runner.EventPermissionTimedOut, log.types)
	}
	if timedOut["on_timeout"] != config.ApprovalOnTimeoutAllow || timedOut["option_kind"] != acpproto.OptionAllowOnce {
		t.Fatalf("timeout detail = %+v, want on_timeout=allow and option_kind=allow_once", timedOut)
	}
	if log.first(runner.EventTurnTimeoutPaused) == nil || log.first(runner.EventTurnTimeoutResumed) == nil {
		t.Fatalf("the clock pause/resume around the approval wait was not recorded (events: %v)", log.types)
	}
}
