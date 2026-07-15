package job

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// pendingCand builds a worker candidate that has negotiated policy push and is still
// waiting to apply rev (PolicyPending). projects/agents are what it CURRENTLY reports —
// its previous, fully-valid config — because a pending worker keeps running that config
// while it applies the new policy; nothing about pending removes a capability.
func pendingCand(id string, projects, agents []string, rev int64) WorkerCandidate {
	return WorkerCandidate{
		WorkerID: id, HeartbeatAge: time.Second,
		Projects: projects, Agents: agents,
		PolicyPending: true, PolicyRev: rev,
	}
}

// TestPolicyPendingInCapsJobStillAccepted is validation 15 (first bullet) and the
// falsification target (H1). A policy_pending worker is STILL running its previous, valid
// config, so a job whose project is in its reported caps must be ACCEPTED — pending must
// never add a rejection.
//
// Falsification (proven manually): make pending a hard reject in validate → this in-caps
// job is rejected by Submit with ErrUnknownProjectOnRunner. That is the exact error
// e.ops.Submit(req) returns inside submitStepFan, whose next line `return err` fails the
// entire workflow (advance.go:365) — the new availability regression the plan forbids.
func TestPolicyPendingInCapsJobStillAccepted(t *testing.T) {
	stub := &stubWorkerRunner{}
	workers := map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}}
	sel := fakeSelector{cands: []WorkerCandidate{
		pendingCand("w1", []string{"self"}, []string{"exec", "term"}, 6),
	}}
	s := newWorkerTestServiceSel(t, t.TempDir(), stub, workers, sel)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
		Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("a policy_pending worker must still run an in-caps job: status=%s err=%s", final.Status, final.Error)
	}
	if stub.gotForward == nil || stub.gotForward.WorkerID != "w1" {
		t.Fatalf("in-caps pending job must be dispatched to w1, got %+v", stub.gotForward)
	}
}

// TestPolicyPendingNotInCapsChangesMessage is validation 15 (second bullet): when the
// project is NOT in the pending worker's caps, the SAME ErrUnknownProjectOnRunner class
// fires (no new path), but the message becomes the explicit policy_pending note (with the
// rev) instead of the misleading "project not on worker" — so an operator retries rather
// than chasing a project the worker will have once it finishes applying the policy.
func TestPolicyPendingNotInCapsChangesMessage(t *testing.T) {
	stub := &stubWorkerRunner{}
	workers := map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}}
	sel := fakeSelector{cands: []WorkerCandidate{
		pendingCand("w1", []string{"other"}, []string{"exec"}, 6), // "self" not yet applied
	}}
	s := newWorkerTestServiceSel(t, t.TempDir(), stub, workers, sel)

	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
		Cmd: []string{"echo", "hi"}, Cwd: ".",
	})
	if !errors.Is(err, ErrUnknownProjectOnRunner) {
		t.Fatalf("must stay the same wrapped error class (HTTP 400), got %v", err)
	}
	if !strings.Contains(err.Error(), "policy_pending") || !strings.Contains(err.Error(), "rev=6") {
		t.Fatalf("pending message must name policy_pending + rev, got %q", err.Error())
	}
	if strings.Contains(err.Error(), "not on worker") {
		t.Fatalf("pending must not use the misleading not-on-worker text: %q", err.Error())
	}
	if stub.gotForward != nil {
		t.Fatalf("rejected job must not be dispatched: %+v", stub.gotForward)
	}
}

// TestNotPendingKeepsOriginalMessage is the control: a NON-pending worker that is simply
// missing the project keeps the original "project not on worker" message. pending only
// swaps the text; it does not alter the (unchanged) rejection for a genuinely absent project.
func TestNotPendingKeepsOriginalMessage(t *testing.T) {
	stub := &stubWorkerRunner{}
	workers := map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}}
	sel := fakeSelector{cands: []WorkerCandidate{
		{WorkerID: "w1", HeartbeatAge: time.Second, Projects: []string{"other"}, Agents: []string{"exec"}}, // PolicyPending=false
	}}
	s := newWorkerTestServiceSel(t, t.TempDir(), stub, workers, sel)

	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1", WorkerID: "w1",
		Cmd: []string{"echo", "hi"}, Cwd: ".",
	})
	if !errors.Is(err, ErrUnknownProjectOnRunner) {
		t.Fatalf("got %v, want ErrUnknownProjectOnRunner", err)
	}
	if !strings.Contains(err.Error(), "not on worker") || strings.Contains(err.Error(), "policy_pending") {
		t.Fatalf("a non-pending worker must keep the original message, got %q", err.Error())
	}
}
