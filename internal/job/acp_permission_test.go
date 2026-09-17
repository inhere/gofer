package job

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp"
	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/store"
)

// The approval gate (GATE-01 §1) is driven end-to-end here: the fake ACP agent raises
// a scripted session/request_permission, and the job either auto-allows it, parks on a
// `permission` interaction for a human, or answers it from the project's timeout
// policy. Every test asserts the OUTCOME THE AGENT OBSERVED (its stderr log records
// each answer) plus the job-level evidence (interactions, events, acp.jsonl).

// submitACPPermissionJob submits the scripted acp-agent prompt WITHOUT waiting: the
// gate may park the job on an interaction, which the caller then answers.
func submitACPPermissionJob(t *testing.T, s *Service, timeoutSec int) string {
	t.Helper()
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local",
		Prompt: acpTestPrompt, Cwd: ".", TimeoutSec: timeoutSec,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return res.ID
}

// waitPermissionInteraction waits for the job's pending `permission` interaction and
// returns it. It also asserts the job is parked in pending_interaction while it waits.
func waitPermissionInteraction(t *testing.T, s *Service, jobID string, d time.Duration) Interaction {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		list, err := s.GetInteractions(jobID)
		if err != nil {
			t.Fatalf("GetInteractions: %v", err)
		}
		for _, it := range list {
			if it.Type == InteractionTypePermission && it.Status == InteractionPending {
				if snap, _ := s.Get(jobID); snap.Status != StatusPendingInteraction {
					t.Fatalf("job status = %s while a permission interaction is pending, want %s",
						snap.Status, StatusPendingInteraction)
				}
				return it
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	snap, _ := s.Get(jobID)
	t.Fatalf("no pending permission interaction within %s (job status=%s)", d, snap.Status)
	return Interaction{}
}

// permAsk is one answer the fake agent's stderr records for a permission request.
type permAsk struct {
	outcome string // selected | cancelled
	option  string // optionId the client answered
	kind    string // the option's kind (derived from the id)
}

// readPermAsks parses the fake agent's permission log from the job's stderr.log — the
// ground truth of what the agent actually received. One entry per ask, in order.
func readPermAsks(t *testing.T, s *Service, root, jobID string) []permAsk {
	t.Helper()
	out, err := store.NewFileStore(filepath.Join(root, "self")).ReadLogTail(jobID, store.StreamStderr, 0)
	if err != nil {
		t.Fatalf("read stderr.log: %v", err)
	}
	var asks []permAsk
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "acptest: permission outcome=") {
			continue
		}
		var a permAsk
		for _, f := range strings.Fields(strings.TrimPrefix(line, "acptest: permission ")) {
			k, v, ok := strings.Cut(f, "=")
			if !ok {
				continue
			}
			switch k {
			case "outcome":
				a.outcome = v
			case "option":
				a.option = v
			case "kind":
				a.kind = v
			}
		}
		asks = append(asks, a)
	}
	return asks
}

// jobEventDetails returns the detail maps of every recorded event of type eventType.
func jobEventDetails(t *testing.T, s *Service, jobID, eventType string) []map[string]any {
	t.Helper()
	events, err := s.ListJobEvents(jobID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	var out []map[string]any
	for _, e := range events {
		if e.Type != eventType {
			continue
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(e.Detail), &d); err != nil {
			t.Fatalf("event %s detail is not JSON: %q", e.Type, e.Detail)
		}
		out = append(out, d)
	}
	return out
}

// acpPermissionRecords returns the acp.jsonl lines with type "permission".
func acpPermissionRecords(t *testing.T, resultDir string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range readACPJSONL(t, resultDir) {
		if l["t"] == "permission" {
			out = append(out, l)
		}
	}
	return out
}

// TestPermissionOffAutoAllows: the default policy (mode unset = off) keeps the S0
// behaviour exactly — the agent's edit request is answered allow_once without any
// interaction and the turn runs to completion.
func TestPermissionOffAutoAllows(t *testing.T) {
	root := t.TempDir()
	s := newACPServiceWith(t, root, acptest.Options{}, &config.ApprovalConfig{Mode: config.ApprovalOff}, "")

	final := acpSubmit(t, s, 30)
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	asks := readPermAsks(t, s, root, final.ID)
	if len(asks) != 1 || asks[0].outcome != "selected" || asks[0].kind != "allow_once" {
		t.Fatalf("agent saw %+v, want one selected allow_once", asks)
	}
	list, err := s.GetInteractions(final.ID)
	if err != nil {
		t.Fatalf("GetInteractions: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("mode=off raised %d interactions, want none: %+v", len(list), list)
	}
	if evs := jobEventDetails(t, s, final.ID, "job.permission_requested"); len(evs) != 0 {
		t.Fatalf("mode=off recorded %d job.permission_requested events, want none", len(evs))
	}
}

// TestPermissionAskAutoAllowsReadKinds: under mode=ask a read-kind tool call is in
// auto_allow_kinds, so it is approved on the spot (allow_once) — no human, no
// interaction, and the turn finishes.
func TestPermissionAskAutoAllowsReadKinds(t *testing.T) {
	root := t.TempDir()
	s := newACPServiceWith(t, root, acptest.Options{PermissionKind: acp.ToolKindRead},
		&config.ApprovalConfig{Mode: config.ApprovalAsk}, "")

	final := acpSubmit(t, s, 30)
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	asks := readPermAsks(t, s, root, final.ID)
	if len(asks) != 1 || asks[0].outcome != "selected" || asks[0].kind != "allow_once" {
		t.Fatalf("agent saw %+v, want one selected allow_once", asks)
	}
	if list, _ := s.GetInteractions(final.ID); len(list) != 0 {
		t.Fatalf("an auto-allowed read raised %d interactions, want none: %+v", len(list), list)
	}
}

// TestPermissionAskCreatesInteractionAndBlocks: an edit-kind tool call under mode=ask
// parks the job in pending_interaction with a fully described approval card (tool call
// + ACP options + policy hint); answering it with allow_once releases the agent,
// which then runs the turn to done.
func TestPermissionAskCreatesInteractionAndBlocks(t *testing.T) {
	root := t.TempDir()
	s := newACPServiceWith(t, root, acptest.Options{}, &config.ApprovalConfig{Mode: config.ApprovalAsk}, "")
	jobID := submitACPPermissionJob(t, s, 30)

	it := waitPermissionInteraction(t, s, jobID, 10*time.Second)
	if it.Prompt == "" {
		t.Error("permission interaction has no prompt")
	}
	if it.ToolCall == nil {
		t.Fatal("permission interaction has no tool_call")
	}
	if it.ToolCall.ID != acptest.ToolCallID || it.ToolCall.Kind != acp.ToolKindEdit {
		t.Errorf("tool_call = %+v, want id=%s kind=%s", it.ToolCall, acptest.ToolCallID, acp.ToolKindEdit)
	}
	if it.ToolCall.Title != acptest.PermissionTitle {
		t.Errorf("tool_call title = %q, want %q", it.ToolCall.Title, acptest.PermissionTitle)
	}
	if !strings.Contains(it.ToolCall.RawInputSummary, "main.go") {
		t.Errorf("raw_input_summary = %q, want it to carry the request's rawInput", it.ToolCall.RawInputSummary)
	}
	if !strings.Contains(it.PolicyHint, config.ApprovalAsk) || !strings.Contains(it.PolicyHint, acp.ToolKindEdit) {
		t.Errorf("policy_hint = %q, want it to name the mode and the kind", it.PolicyHint)
	}
	// The options are the AGENT's, verbatim: my answer is one of their optionIds.
	var kinds []string
	for _, o := range it.Options {
		if o.ID == "" || o.ID != o.Value {
			t.Errorf("option %+v: id must be the ACP optionId (and the answer token)", o)
		}
		kinds = append(kinds, o.Kind)
	}
	if got, want := strings.Join(kinds, ","), "allow_once,allow_always,reject_once"; got != want {
		t.Fatalf("option kinds = %q, want %q", got, want)
	}

	// The approval is announced for the notification/audit layer.
	reqs := jobEventDetails(t, s, jobID, "job.permission_requested")
	if len(reqs) != 1 {
		t.Fatalf("job.permission_requested events = %d, want 1", len(reqs))
	}
	if reqs[0]["interaction_id"] != it.ID || reqs[0]["kind"] != acp.ToolKindEdit {
		t.Errorf("job.permission_requested detail = %+v, want interaction_id=%s kind=%s", reqs[0], it.ID, acp.ToolKindEdit)
	}

	// The agent is BLOCKED until we answer: the turn cannot have progressed.
	if final, _ := s.Get(jobID); final.Status != StatusPendingInteraction {
		t.Fatalf("job status = %s before the answer, want %s", final.Status, StatusPendingInteraction)
	}
	if asks := readPermAsks(t, s, root, jobID); len(asks) != 0 {
		t.Fatalf("the agent already got an answer while the card was pending: %+v", asks)
	}

	if _, err := s.AnswerInteractionByHuman(jobID, it.ID, acptest.AllowOnceOptionID, ""); err != nil {
		t.Fatalf("AnswerInteractionByHuman: %v", err)
	}
	final, ok := s.Wait(jobID)
	if !ok {
		t.Fatalf("job %s not found", jobID)
	}
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	asks := readPermAsks(t, s, root, jobID)
	if len(asks) != 1 || asks[0].option != acptest.AllowOnceOptionID || asks[0].outcome != "selected" {
		t.Fatalf("agent saw %+v, want selected %s", asks, acptest.AllowOnceOptionID)
	}

	// The audit trail: the answer (who + which option) and the acp.jsonl record.
	answered := jobEventDetails(t, s, jobID, "job.permission_answered")
	if len(answered) != 1 || answered[0]["option_id"] != acptest.AllowOnceOptionID ||
		answered[0]["by"] != "human" || answered[0]["auto"] != false {
		t.Fatalf("job.permission_answered = %+v, want option_id=%s by=human auto=false", answered, acptest.AllowOnceOptionID)
	}
	recs := acpPermissionRecords(t, final.ResultDir)
	if len(recs) != 1 || recs[0]["outcome"] != "selected" || recs[0]["option_id"] != acptest.AllowOnceOptionID {
		t.Fatalf("acp.jsonl permission records = %+v, want one selected %s", recs, acptest.AllowOnceOptionID)
	}
}

// TestPermissionRejectStopsToolCall: a human rejection is relayed as `selected` with
// the agent's reject option, so the AGENT decides what a refusal means — here it
// abandons the turn (stopReason refusal → failed job).
func TestPermissionRejectStopsToolCall(t *testing.T) {
	root := t.TempDir()
	s := newACPServiceWith(t, root, acptest.Options{}, &config.ApprovalConfig{Mode: config.ApprovalAsk}, "")
	jobID := submitACPPermissionJob(t, s, 30)

	it := waitPermissionInteraction(t, s, jobID, 10*time.Second)
	if _, err := s.AnswerInteractionByHuman(jobID, it.ID, acptest.RejectOnceOptionID, ""); err != nil {
		t.Fatalf("AnswerInteractionByHuman: %v", err)
	}
	final, _ := s.Wait(jobID)
	if final.Status != StatusFailed {
		t.Fatalf("status = %s (err=%s), want failed", final.Status, final.Error)
	}
	if final.StopReason != acp.StopRefusal {
		t.Fatalf("stop_reason = %q, want %q", final.StopReason, acp.StopRefusal)
	}
	asks := readPermAsks(t, s, root, jobID)
	if len(asks) != 1 || asks[0].option != acptest.RejectOnceOptionID || asks[0].outcome != "selected" {
		t.Fatalf("agent saw %+v, want selected %s (a rejection is the agent's to interpret)",
			asks, acptest.RejectOnceOptionID)
	}
}

// TestPermissionTimeoutRejects: nobody answers within approval.timeout_sec, so the
// pending card is closed and the agent is told reject_once (on_timeout: reject) —
// the default must never turn silence into an approval.
func TestPermissionTimeoutRejects(t *testing.T) {
	root := t.TempDir()
	s := newACPServiceWith(t, root, acptest.Options{}, &config.ApprovalConfig{
		Mode: config.ApprovalAsk, TimeoutSec: 1,
	}, "")
	jobID := submitACPPermissionJob(t, s, 30)

	it := waitPermissionInteraction(t, s, jobID, 10*time.Second)
	final, _ := s.Wait(jobID)
	if final.Status != StatusFailed {
		t.Fatalf("status = %s (err=%s), want failed (the agent refused after the timeout)", final.Status, final.Error)
	}
	asks := readPermAsks(t, s, root, jobID)
	if len(asks) != 1 || asks[0].option != acptest.RejectOnceOptionID {
		t.Fatalf("agent saw %+v, want the timed-out request answered with %s", asks, acptest.RejectOnceOptionID)
	}
	timeouts := jobEventDetails(t, s, jobID, "job.permission_timed_out")
	if len(timeouts) != 1 || timeouts[0]["interaction_id"] != it.ID {
		t.Fatalf("job.permission_timed_out = %+v, want one for interaction %s", timeouts, it.ID)
	}
	// The card must not stay pending forever: the job is no longer parked on it.
	got, err := s.GetInteractions(jobID)
	if err != nil {
		t.Fatalf("GetInteractions: %v", err)
	}
	if len(got) != 1 || got[0].Status != InteractionCancelled {
		t.Fatalf("timed-out interaction = %+v, want a single cancelled one", got)
	}
}

// TestPermissionTimeoutAllowWhenConfigured: on_timeout: allow answers a timed-out
// request with allow_once, so an unattended project can let a known-safe kind run
// unattended. The card is still closed and the timeout still recorded.
func TestPermissionTimeoutAllowWhenConfigured(t *testing.T) {
	root := t.TempDir()
	s := newACPServiceWith(t, root, acptest.Options{}, &config.ApprovalConfig{
		Mode: config.ApprovalAsk, TimeoutSec: 1, OnTimeout: config.ApprovalOnTimeoutAllow,
	}, "")
	jobID := submitACPPermissionJob(t, s, 30)

	waitPermissionInteraction(t, s, jobID, 10*time.Second)
	final, _ := s.Wait(jobID)
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done (the agent was allowed to continue)", final.Status, final.Error)
	}
	asks := readPermAsks(t, s, root, jobID)
	if len(asks) != 1 || asks[0].option != acptest.AllowOnceOptionID || asks[0].outcome != "selected" {
		t.Fatalf("agent saw %+v, want selected %s", asks, acptest.AllowOnceOptionID)
	}
	if len(jobEventDetails(t, s, jobID, "job.permission_timed_out")) != 1 {
		t.Fatalf("no job.permission_timed_out event for the timed-out approval")
	}
}

// TestPermissionAllowAlwaysRemembered: an allow_always answer covers the SAME tool
// kind for the rest of the job — the agent's second identical request is approved
// without asking again (remember_allow_always defaults to true).
func TestPermissionAllowAlwaysRemembered(t *testing.T) {
	root := t.TempDir()
	s := newACPServiceWith(t, root, acptest.Options{PermissionRepeats: 2},
		&config.ApprovalConfig{Mode: config.ApprovalAsk}, "")
	jobID := submitACPPermissionJob(t, s, 30)

	it := waitPermissionInteraction(t, s, jobID, 10*time.Second)
	if _, err := s.AnswerInteractionByHuman(jobID, it.ID, acptest.AllowAlwaysOptionID, ""); err != nil {
		t.Fatalf("AnswerInteractionByHuman: %v", err)
	}
	final, _ := s.Wait(jobID)
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	asks := readPermAsks(t, s, root, jobID)
	if len(asks) != 2 {
		t.Fatalf("agent made %d asks, want 2: %+v", len(asks), asks)
	}
	if asks[0].option != acptest.AllowAlwaysOptionID || asks[1].option != acptest.AllowAlwaysOptionID {
		t.Fatalf("agent saw %+v, want the remembered allow_always for both asks", asks)
	}
	// The second ask was answered by the remembered policy, not by a human: one
	// interaction total, and the auto answer is visible in the audit trail.
	list, _ := s.GetInteractions(jobID)
	if len(list) != 1 {
		t.Fatalf("interactions = %d, want exactly one (the second ask is remembered): %+v", len(list), list)
	}
	answered := jobEventDetails(t, s, jobID, "job.permission_answered")
	if len(answered) != 2 {
		t.Fatalf("job.permission_answered = %+v, want 2 (human + remembered)", answered)
	}
	if answered[1]["auto"] != true || answered[1]["option_id"] != acptest.AllowAlwaysOptionID {
		t.Fatalf("second answer = %+v, want auto=true option_id=%s", answered[1], acptest.AllowAlwaysOptionID)
	}
	if reqs := jobEventDetails(t, s, jobID, "job.permission_requested"); len(reqs) != 1 {
		t.Fatalf("job.permission_requested = %d, want 1 (the remembered ask raised no card)", len(reqs))
	}
}

// TestPermissionStrictAsksForReads: mode=strict ignores the kind lists entirely —
// even a read is put to a human (the way to run an agent with no pre-approved kinds).
func TestPermissionStrictAsksForReads(t *testing.T) {
	root := t.TempDir()
	s := newACPServiceWith(t, root, acptest.Options{PermissionKind: acp.ToolKindRead},
		&config.ApprovalConfig{Mode: config.ApprovalStrict}, "")
	jobID := submitACPPermissionJob(t, s, 30)

	it := waitPermissionInteraction(t, s, jobID, 10*time.Second)
	if it.ToolCall == nil || it.ToolCall.Kind != acp.ToolKindRead {
		t.Fatalf("tool_call = %+v, want kind=read", it.ToolCall)
	}
	if _, err := s.AnswerInteractionByHuman(jobID, it.ID, acptest.AllowOnceOptionID, ""); err != nil {
		t.Fatalf("AnswerInteractionByHuman: %v", err)
	}
	final, _ := s.Wait(jobID)
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	asks := readPermAsks(t, s, root, jobID)
	if len(asks) != 1 || asks[0].option != acptest.AllowOnceOptionID {
		t.Fatalf("agent saw %+v, want selected %s", asks, acptest.AllowOnceOptionID)
	}
}

// TestPermissionAgentPolicyTightensProject: an agent's acp.permission_policy is the
// second half of the gate — with the PROJECT at its default (off), an agent-level
// `ask` still parks the edit request on a human.
func TestPermissionAgentPolicyTightensProject(t *testing.T) {
	root := t.TempDir()
	s := newACPServiceWith(t, root, acptest.Options{}, nil, config.ApprovalAsk)
	jobID := submitACPPermissionJob(t, s, 30)

	it := waitPermissionInteraction(t, s, jobID, 10*time.Second)
	if _, err := s.AnswerInteractionByHuman(jobID, it.ID, acptest.AllowOnceOptionID, ""); err != nil {
		t.Fatalf("AnswerInteractionByHuman: %v", err)
	}
	final, _ := s.Wait(jobID)
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if asks := readPermAsks(t, s, root, jobID); len(asks) != 1 || asks[0].outcome != "selected" {
		t.Fatalf("agent saw %+v, want one selected answer", asks)
	}
}

// TestPermissionInteractionSurvivesJobCancel: the approval wait is bounded by the
// JOB's context too — cancelling a job parked on an approval must not hang the runner
// (the ACP client gets a cancellation and the turn unwinds as cancelled).
func TestPermissionInteractionSurvivesJobCancel(t *testing.T) {
	root := t.TempDir()
	s := newACPServiceWith(t, root, acptest.Options{}, &config.ApprovalConfig{
		Mode: config.ApprovalAsk, TimeoutSec: 300,
	}, "")
	jobID := submitACPPermissionJob(t, s, 300)
	waitPermissionInteraction(t, s, jobID, 10*time.Second)

	if err := s.Cancel(jobID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	final, ok := s.Wait(jobID)
	if !ok {
		t.Fatalf("job %s not found", jobID)
	}
	if final.Status != StatusCancelled {
		t.Fatalf("status = %s (err=%s), want cancelled", final.Status, final.Error)
	}
	// The abandoned card is reconciled to cancelled by finish(), so no zombie pending
	// row survives to be answered.
	list, err := s.GetInteractions(jobID)
	if err != nil {
		t.Fatalf("GetInteractions: %v", err)
	}
	for _, it := range list {
		if it.Status == InteractionPending {
			t.Fatalf("interaction %s is still pending after the job was cancelled", it.ID)
		}
	}
}

// TestPermissionWaitAnswerIntegration guards the sink's contract directly: the
// approval request blocks until the interaction is answered (no polling inside the
// runner), which is what makes the gate a real gate.
func TestPermissionWaitAnswerIntegration(t *testing.T) {
	s := newTestService(t, t.TempDir())
	jobID := submitRunning(t, s)

	it, err := s.CreateInteraction(jobID, InteractionInput{
		Type:   InteractionTypePermission,
		Prompt: "approve?",
		Options: []InteractionOption{
			{Value: acptest.AllowOnceOptionID, ID: acptest.AllowOnceOptionID, Kind: acp.OptionAllowOnce},
		},
	})
	if err != nil {
		t.Fatalf("CreateInteraction(permission): %v", err)
	}

	got := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, werr := s.WaitAnswer(ctx, jobID, it.ID)
		got <- werr
	}()
	select {
	case werr := <-got:
		t.Fatalf("WaitAnswer returned %v before any answer", werr)
	case <-time.After(50 * time.Millisecond):
	}

	if _, err := s.AnswerInteractionByHuman(jobID, it.ID, acptest.AllowOnceOptionID, "alice"); err != nil {
		t.Fatalf("AnswerInteractionByHuman: %v", err)
	}
	select {
	case werr := <-got:
		if werr != nil {
			t.Fatalf("WaitAnswer: %v", werr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitAnswer did not return after the answer")
	}
}
