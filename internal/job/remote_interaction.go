package job

import (
	"context"
	"fmt"

	"dev-agent-bridge/internal/runner"
)

// remoteInteractionSink implements runner.InteractionSink for a remote-runner
// (peer-http) job: it injects peer-raised interactions onto the host job and hands
// the runner a channel carrying the host-side answer.
type remoteInteractionSink struct {
	s     *Service
	jobID string
}

// Open records the peer-raised interaction on the host job (flipping it to
// pending_interaction) and returns a channel that yields the host-side answer once
// the user answers. The channel is closed without a value if the job ends / ctx is
// cancelled first (so the runner never blocks forever).
func (k remoteInteractionSink) Open(ctx context.Context, ri runner.RemoteInteraction) (<-chan string, error) {
	it := Interaction{
		ID:        ri.ID,
		JobID:     k.jobID,
		Type:      ri.Type,
		Prompt:    ri.Prompt,
		Options:   fromRemoteOptions(ri.Options),
		Status:    InteractionPending,
		CreatedAt: k.s.nowFn().Unix(),
	}
	if err := k.s.injectInteraction(k.jobID, it); err != nil {
		return nil, err
	}
	ch := make(chan string, 1)
	go func() {
		defer close(ch)
		ans, err := k.s.WaitAnswer(ctx, k.jobID, ri.ID)
		if err == nil && ans.Status == InteractionAnswered {
			ch <- ans.Answer
		}
	}()
	return ch, nil
}

// fromRemoteOptions converts neutral runner options into job options (nil-safe).
func fromRemoteOptions(in []runner.RemoteInteractionOption) []InteractionOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]InteractionOption, 0, len(in))
	for _, o := range in {
		out = append(out, InteractionOption{Value: o.Value, Label: o.Label})
	}
	return out
}

// injectInteraction records a pre-constructed interaction (carrying the PEER's id)
// on a live host job and flips it to pending_interaction. Unlike CreateInteraction
// it performs no type/prompt validation (the peer already validated) and is
// idempotent: a repeated id (the peer may resend `open`) is a no-op. Errors if the
// job is unknown or already terminal.
func (s *Service) injectInteraction(jobID string, it Interaction) error {
	entry := s.entry(jobID)
	if entry == nil {
		return s.notLiveErr(jobID, "inject")
	}

	entry.mu.Lock()
	if IsTerminal(entry.result.Status) {
		status := entry.result.Status
		entry.mu.Unlock()
		return fmt.Errorf("%w: job %q (%s): cannot inject interaction", ErrJobTerminal, jobID, status)
	}
	// Idempotent: the peer may resend `open` for an interaction we already bridged.
	if findInteraction(entry.interactions, it.ID) != nil {
		entry.mu.Unlock()
		return nil
	}

	entry.interactions = append(entry.interactions, &interactionRec{
		data:     it,
		answered: make(chan struct{}),
	})
	entry.result.Status = StatusPendingInteraction
	snap := entry.result
	entry.mu.Unlock()

	// Persist outside the lock, mirroring CreateInteraction: the job snapshot and
	// the pending interaction row, both into the SQLite metadata store (best-effort).
	_ = s.persist(snap)
	_ = s.meta.UpsertInteraction(toInteractionRecord(it))

	return nil
}
