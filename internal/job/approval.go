package job

import (
	"context"
	"log/slog"

	"github.com/inhere/gofer/internal/runner"
)

// approvalSink implements runner.ApprovalSink for a job executed in THIS process
// (GATE-01 §1): it raises the approval as an interaction of type permission — so the
// card reaches every existing answer surface (web, CLI, MCP, the worker→hub mirror)
// with no new plumbing — and blocks until it is answered, the gate's deadline passes,
// or the job ends.
type approvalSink struct {
	s     *Service
	jobID string
}

// RequestApproval raises the interaction and waits for the answer. A ctx that ends
// first (the gate's own approval deadline, the job's timeout, or a cancel) closes the
// abandoned card — the job must not stay parked on a question the agent has already
// stopped waiting for — and returns the ctx error, leaving the caller to apply the
// policy for a timed-out request.
func (k approvalSink) RequestApproval(ctx context.Context, req runner.ApprovalRequest) (runner.ApprovalOutcome, error) {
	it, err := k.s.CreateInteraction(k.jobID, InteractionInput{
		Type:       InteractionTypePermission,
		Prompt:     req.Prompt,
		Options:    approvalOptions(req.Options),
		ToolCall:   approvalToolCall(req.ToolCall),
		PolicyHint: req.PolicyHint,
		// The requester's own deadline, so the card can count down to the moment the
		// gate gives up (and the answer paths can see how long is left).
		ExpiresAt: approvalDeadline(k.s.nowFn().Unix(), req.TimeoutSec),
	})
	if err != nil {
		return runner.ApprovalOutcome{}, err
	}
	// The card exists now: tell the caller before we start waiting, so its
	// "needs approval" event/notification goes out while the request is still open.
	if req.OnRaised != nil {
		req.OnRaised(it.ID)
	}
	ans, werr := k.s.WaitAnswer(ctx, k.jobID, it.ID)
	if werr != nil {
		if cerr := k.s.cancelInteraction(k.jobID, it.ID); cerr != nil {
			// Best-effort: the runner is unwinding either way, and finish() reconciles
			// any row this missed.
			slog.Debug("approval: cancel abandoned interaction",
				"job_id", k.jobID, "interaction_id", it.ID, "err", cerr)
		}
		return runner.ApprovalOutcome{}, werr
	}
	if ans.Status != InteractionAnswered {
		// The interaction was cancelled underneath us (the job finished): no answer.
		return runner.ApprovalOutcome{}, nil
	}
	return runner.ApprovalOutcome{Answer: ans.Answer, By: ans.AnsweredBy}, nil
}

// approvalDeadline converts the request's timeout into an absolute unix-second
// deadline (0 when the request has none, so "no countdown" stays distinguishable).
func approvalDeadline(now int64, timeoutSec int) int64 {
	if timeoutSec <= 0 {
		return 0
	}
	return now + int64(timeoutSec)
}

// approvalOptions projects the agent's options onto the interaction's. The ACP
// optionId becomes BOTH the option's id and its value: the value is the answer token
// every generic answer path round-trips, and the id is the protocol identity a
// card/report speaks about.
func approvalOptions(in []runner.ApprovalOption) []InteractionOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]InteractionOption, 0, len(in))
	for _, o := range in {
		out = append(out, InteractionOption{Value: o.ID, ID: o.ID, Label: o.Label, Kind: o.Kind})
	}
	return out
}

// approvalToolCall projects the gated tool call (nil-safe).
func approvalToolCall(in *runner.ApprovalToolCall) *InteractionToolCall {
	if in == nil {
		return nil
	}
	return &InteractionToolCall{
		ID:              in.ID,
		Title:           in.Title,
		Kind:            in.Kind,
		Locations:       in.Locations,
		RawInputSummary: in.RawInputSummary,
	}
}

// fromRemoteToolCall projects a mirrored tool call (nil-safe).
func fromRemoteToolCall(in *runner.RemoteInteractionToolCall) *InteractionToolCall {
	if in == nil {
		return nil
	}
	return &InteractionToolCall{
		ID:              in.ID,
		Title:           in.Title,
		Kind:            in.Kind,
		Locations:       in.Locations,
		RawInputSummary: in.RawInputSummary,
	}
}
