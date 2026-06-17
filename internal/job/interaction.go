package job

import (
	"context"
	"encoding/json"
	"fmt"
)

// InteractionOption is one selectable option for a choice/confirmation.
type InteractionOption struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// Interaction is one running-job interaction event (plan §P9). Persisted as an
// append snapshot to <job_dir>/interactions.jsonl; the latest snapshot per id wins.
type Interaction struct {
	ID         string              `json:"id"`
	JobID      string              `json:"job_id"`
	Type       string              `json:"type"` // question | choice | confirmation
	Prompt     string              `json:"prompt"`
	Options    []InteractionOption `json:"options,omitempty"`
	Status     string              `json:"status"` // pending | answered | cancelled
	Answer     string              `json:"answer,omitempty"`
	CreatedAt  int64               `json:"created_at"`
	AnsweredAt int64               `json:"answered_at,omitempty"`
}

// Interaction status values.
const (
	InteractionPending   = "pending"
	InteractionAnswered  = "answered"
	InteractionCancelled = "cancelled"
)

// Interaction type values.
const (
	InteractionTypeQuestion     = "question"
	InteractionTypeChoice       = "choice"
	InteractionTypeConfirmation = "confirmation"
)

// interactionRec is the in-process record for one interaction: the persisted
// Interaction value plus a channel closed once it is answered/cancelled, which
// wakes any WaitAnswer caller. It is mutated only under the owning jobEntry.mu.
type interactionRec struct {
	data Interaction
	// answered is closed exactly once when the interaction leaves pending; closing
	// (not sending) lets any number of WaitAnswer callers observe it.
	answered chan struct{}
}

// InteractionInput is the create-interaction payload (Type/Prompt/Options).
type InteractionInput struct {
	Type    string
	Prompt  string
	Options []InteractionOption
}

// CreateInteraction raises a new interaction on a LIVE job: it records a pending
// Interaction, flips the job status to pending_interaction and persists. Errors
// if the job is unknown or already terminal.
func (s *Service) CreateInteraction(jobID string, in InteractionInput) (Interaction, error) {
	entry := s.entry(jobID)
	if entry == nil {
		return Interaction{}, fmt.Errorf("unknown job %q", jobID)
	}

	entry.mu.Lock()
	// Refuse to raise an interaction on a job that already reached a terminal
	// state: there is no live agent left to consume the answer.
	if IsTerminal(entry.result.Status) {
		status := entry.result.Status
		entry.mu.Unlock()
		return Interaction{}, fmt.Errorf("job %q is terminal (%s): cannot create interaction", jobID, status)
	}

	rec := &interactionRec{
		data: Interaction{
			ID:        randomSuffix(),
			JobID:     jobID,
			Type:      in.Type,
			Prompt:    in.Prompt,
			Options:   in.Options,
			Status:    InteractionPending,
			CreatedAt: s.nowFn().Unix(),
		},
		answered: make(chan struct{}),
	}
	entry.interactions = append(entry.interactions, rec)
	// A pending interaction blocks the agent; surface that in the job status so
	// the UI/poller can prompt the user. The terminal guard above plus the lock
	// shared with finish() guarantee we never override a terminal status here.
	entry.result.Status = StatusPendingInteraction
	snap := entry.result
	out := rec.data
	entry.mu.Unlock()

	// Persist outside callers' view of the lock: result.json + index snapshot
	// (best-effort, sharing the service indexMu) and the pending interaction line.
	_ = entry.store.WriteResult(jobID, snap)
	s.appendIndex(entry.store, snap)
	_ = entry.store.AppendInteraction(jobID, out)

	return out, nil
}

// GetInteractions returns the job's interactions (latest snapshot per id, in
// creation order). When the job is still in memory the in-process state is
// authoritative; otherwise it returns an empty slice (the per-project result
// base cannot be derived from the id alone — use GetPersistedInteractions with
// the base for the after-restart fallback). Unknown in-memory job => empty.
func (s *Service) GetInteractions(jobID string) ([]Interaction, error) {
	if entry := s.entry(jobID); entry != nil {
		entry.mu.Lock()
		defer entry.mu.Unlock()
		out := make([]Interaction, 0, len(entry.interactions))
		for _, rec := range entry.interactions {
			out = append(out, rec.data) // value copy: never leak *interactionRec
		}
		return out, nil
	}
	return []Interaction{}, nil
}

// GetPersistedInteractions returns a job's interactions, preferring the live
// in-memory state and falling back to folding interactions.jsonl on disk when
// the job is not tracked in this process (e.g. after a restart). base is the
// result base dir for the job's project. A missing file yields an empty slice.
func (s *Service) GetPersistedInteractions(base, jobID string) ([]Interaction, error) {
	if entry := s.entry(jobID); entry != nil {
		entry.mu.Lock()
		defer entry.mu.Unlock()
		out := make([]Interaction, 0, len(entry.interactions))
		for _, rec := range entry.interactions {
			out = append(out, rec.data)
		}
		return out, nil
	}
	lines, err := s.newStore(base).ReadInteractions(jobID)
	if err != nil {
		return nil, err
	}
	return foldInteractions(lines)
}

// foldInteractions collapses append snapshots by interaction id, keeping the
// last snapshot for each id while preserving the order ids first appeared.
func foldInteractions(lines []json.RawMessage) ([]Interaction, error) {
	order := make([]string, 0, len(lines))
	byID := make(map[string]Interaction, len(lines))
	for _, line := range lines {
		var it Interaction
		if err := json.Unmarshal(line, &it); err != nil {
			continue // tolerate an unparseable snapshot (corrupt tail)
		}
		if _, seen := byID[it.ID]; !seen {
			order = append(order, it.ID)
		}
		byID[it.ID] = it
	}
	out := make([]Interaction, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, nil
}

// AnswerInteraction marks a pending interaction answered, records the answer,
// wakes any waiter, and — when no pending interactions remain — flips the job
// status back to running (never overriding a terminal status). Errors if job/
// interaction unknown or the interaction is not pending.
func (s *Service) AnswerInteraction(jobID, interactionID, answer string) (Interaction, error) {
	entry := s.entry(jobID)
	if entry == nil {
		return Interaction{}, fmt.Errorf("unknown job %q", jobID)
	}

	entry.mu.Lock()
	// A terminal job has no live agent waiting on the answer; reject so the
	// caller knows it has no effect.
	if IsTerminal(entry.result.Status) {
		status := entry.result.Status
		entry.mu.Unlock()
		return Interaction{}, fmt.Errorf("job %q is terminal (%s): cannot answer interaction", jobID, status)
	}

	rec := findInteraction(entry.interactions, interactionID)
	if rec == nil {
		entry.mu.Unlock()
		return Interaction{}, fmt.Errorf("unknown interaction %q for job %q", interactionID, jobID)
	}
	if rec.data.Status != InteractionPending {
		st := rec.data.Status
		entry.mu.Unlock()
		return Interaction{}, fmt.Errorf("interaction %q is not pending (%s)", interactionID, st)
	}

	rec.data.Status = InteractionAnswered
	rec.data.Answer = answer
	rec.data.AnsweredAt = s.nowFn().Unix()
	out := rec.data

	// Only return the job to running when no other interaction is still pending
	// AND the job is currently parked in pending_interaction (don't resurrect a
	// job whose status drifted for another reason).
	var resumeSnap *JobResult
	if !hasPendingInteraction(entry.interactions) && entry.result.Status == StatusPendingInteraction {
		entry.result.Status = StatusRunning
		snap := entry.result
		resumeSnap = &snap
	}
	entry.mu.Unlock()

	// Persist the answered snapshot first; then, if we resumed, the running
	// result snapshot + index line.
	_ = entry.store.AppendInteraction(jobID, out)
	if resumeSnap != nil {
		_ = entry.store.WriteResult(jobID, *resumeSnap)
		s.appendIndex(entry.store, *resumeSnap)
	}
	// Wake any WaitAnswer caller. Closing under no lock is fine: the channel is
	// closed exactly once (the pending->answered transition is single-shot).
	close(rec.answered)

	return out, nil
}

// WaitAnswer blocks until the interaction is answered/cancelled or ctx is done,
// returning the final Interaction. Used by in-process wrappers/tests.
func (s *Service) WaitAnswer(ctx context.Context, jobID, interactionID string) (Interaction, error) {
	entry := s.entry(jobID)
	if entry == nil {
		return Interaction{}, fmt.Errorf("unknown job %q", jobID)
	}

	entry.mu.Lock()
	rec := findInteraction(entry.interactions, interactionID)
	if rec == nil {
		entry.mu.Unlock()
		return Interaction{}, fmt.Errorf("unknown interaction %q for job %q", interactionID, jobID)
	}
	ch := rec.answered
	// Already resolved: return immediately without blocking.
	if rec.data.Status != InteractionPending {
		out := rec.data
		entry.mu.Unlock()
		return out, nil
	}
	entry.mu.Unlock()

	select {
	case <-ch:
		entry.mu.Lock()
		out := rec.data
		entry.mu.Unlock()
		return out, nil
	case <-ctx.Done():
		return Interaction{}, ctx.Err()
	}
}

// findInteraction returns the rec with the given id, or nil. Caller holds mu.
func findInteraction(recs []*interactionRec, id string) *interactionRec {
	for _, rec := range recs {
		if rec.data.ID == id {
			return rec
		}
	}
	return nil
}

// hasPendingInteraction reports whether any interaction is still pending. Caller
// holds mu.
func hasPendingInteraction(recs []*interactionRec) bool {
	for _, rec := range recs {
		if rec.data.Status == InteractionPending {
			return true
		}
	}
	return false
}
