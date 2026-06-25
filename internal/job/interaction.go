package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
)

// Interaction error sentinels. The interaction methods wrap these (via
// fmt.Errorf("%w: ...")) so the HTTP layer can map each failure to a stable
// status code with errors.Is, without string-matching the message.
var (
	// ErrUnknownJob — no job with the given id is tracked. HTTP: 404.
	ErrUnknownJob = errors.New("unknown job")
	// ErrUnknownInteraction — the job has no interaction with the given id. HTTP: 404.
	ErrUnknownInteraction = errors.New("unknown interaction")
	// ErrJobTerminal — the job already reached a terminal state, so there is no
	// live agent to consume an interaction/answer. HTTP: 409.
	ErrJobTerminal = errors.New("job is terminal")
	// ErrInteractionState — answering an interaction that is not pending (already
	// answered/cancelled). HTTP: 400.
	ErrInteractionState = errors.New("interaction not pending")
	// ErrInvalidInteraction — the create payload failed basic validation (bad type
	// or empty prompt). HTTP: 400.
	ErrInvalidInteraction = errors.New("invalid interaction")
)

// InteractionOption is one selectable option for a choice/confirmation.
type InteractionOption struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// Interaction is one running-job interaction event (plan §P9). Persisted (SP4)
// as a single upserted row in the SQLite interactions table (the latest snapshot
// per id wins); the live in-memory state in jobEntry is authoritative while the
// job is tracked.
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
	// Basic payload validation up front (before touching any job state): an empty
	// Type defaults to question; otherwise it must be a known interaction type.
	// Prompt must be non-empty after trimming whitespace. choice/confirmation
	// SHOULD carry options but it is not enforced here (the caller may add them
	// later, and a confirmation often implies yes/no without explicit options).
	in.Type = strings.TrimSpace(in.Type)
	if in.Type == "" {
		in.Type = InteractionTypeQuestion
	}
	switch in.Type {
	case InteractionTypeQuestion, InteractionTypeChoice, InteractionTypeConfirmation:
	default:
		return Interaction{}, fmt.Errorf("%w: unknown type %q", ErrInvalidInteraction, in.Type)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return Interaction{}, fmt.Errorf("%w: prompt must not be empty", ErrInvalidInteraction)
	}

	entry := s.entry(jobID)
	if entry == nil {
		return Interaction{}, s.notLiveErr(jobID, "create")
	}

	entry.mu.Lock()
	// Refuse to raise an interaction on a job that already reached a terminal
	// state: there is no live agent left to consume the answer.
	if IsTerminal(entry.result.Status) {
		status := entry.result.Status
		entry.mu.Unlock()
		return Interaction{}, fmt.Errorf("%w: job %q (%s): cannot create interaction", ErrJobTerminal, jobID, status)
	}

	rec := &interactionRec{
		data: Interaction{
			ID:        RandomSuffix(),
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

	// Persist outside callers' view of the lock: the job snapshot and the pending
	// interaction row, both into the SQLite metadata store (best-effort).
	_ = s.persist(snap)
	_ = s.meta.UpsertInteraction(toInteractionRecord(out))

	// E13: record the interaction.created lifecycle event after persistence.
	s.recordEvent(jobID, EventInteractionCreated, map[string]any{
		"interaction_id": out.ID,
		"type":           out.Type,
		"prompt":         out.Prompt,
	})

	return out, nil
}

// toInteractionRecord projects a job Interaction onto the neutral
// jobstore.InteractionRecord written to SQLite. Options are marshalled to
// OptionsJSON (empty options -> empty string, so the column stays NULL/empty).
func toInteractionRecord(it Interaction) jobstore.InteractionRecord {
	var optsJSON string
	if len(it.Options) > 0 {
		if b, err := json.Marshal(it.Options); err == nil {
			optsJSON = string(b)
		}
	}
	return jobstore.InteractionRecord{
		ID:          it.ID,
		JobID:       it.JobID,
		Type:        it.Type,
		Prompt:      it.Prompt,
		OptionsJSON: optsJSON,
		Status:      it.Status,
		Answer:      it.Answer,
		CreatedAt:   it.CreatedAt,
		AnsweredAt:  it.AnsweredAt,
	}
}

// fromInteractionRecord rebuilds a job Interaction from a persisted
// jobstore.InteractionRecord. OptionsJSON is unmarshalled back into Options (an
// empty string yields nil Options); an unparseable blob leaves Options nil rather
// than failing the read.
func fromInteractionRecord(rec jobstore.InteractionRecord) Interaction {
	var opts []InteractionOption
	if rec.OptionsJSON != "" {
		_ = json.Unmarshal([]byte(rec.OptionsJSON), &opts)
	}
	return Interaction{
		ID:         rec.ID,
		JobID:      rec.JobID,
		Type:       rec.Type,
		Prompt:     rec.Prompt,
		Options:    opts,
		Status:     rec.Status,
		Answer:     rec.Answer,
		CreatedAt:  rec.CreatedAt,
		AnsweredAt: rec.AnsweredAt,
	}
}

// GetInteractions returns the job's interactions (latest snapshot per id, in
// creation order). When the job is still in memory (live) the in-process state is
// authoritative — it carries the pending-channel state WaitAnswer relies on.
// Otherwise (evicted after finishing, or never tracked in this process) it falls
// back to the SQLite interactions table via ListInteractions, which surfaces the
// terminal job's answered history. An unknown job yields an empty slice.
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
	recs, err := s.meta.ListInteractions(jobID)
	if err != nil {
		return nil, err
	}
	out := make([]Interaction, 0, len(recs))
	for _, rec := range recs {
		out = append(out, fromInteractionRecord(rec))
	}
	return out, nil
}

// GetPersistedInteractions returns a job's interactions, preferring the live
// in-memory state and falling back to the SQLite interactions table when the job
// is not tracked in this process (evicted/after a restart). The SQLite store is a
// single global DB, so base is no longer needed (kept in the signature to avoid
// touching callers); this simply delegates to GetInteractions.
func (s *Service) GetPersistedInteractions(_ string, jobID string) ([]Interaction, error) {
	return s.GetInteractions(jobID)
}

// AnswerInteraction marks a pending interaction answered, records the answer,
// wakes any waiter, and — when no pending interactions remain — flips the job
// status back to running (never overriding a terminal status). Errors if job/
// interaction unknown or the interaction is not pending.
func (s *Service) AnswerInteraction(jobID, interactionID, answer string) (Interaction, error) {
	entry := s.entry(jobID)
	if entry == nil {
		return Interaction{}, s.notLiveErr(jobID, "answer")
	}

	entry.mu.Lock()
	// A terminal job has no live agent waiting on the answer; reject so the
	// caller knows it has no effect.
	if IsTerminal(entry.result.Status) {
		status := entry.result.Status
		entry.mu.Unlock()
		return Interaction{}, fmt.Errorf("%w: job %q (%s): cannot answer interaction", ErrJobTerminal, jobID, status)
	}

	rec := findInteraction(entry.interactions, interactionID)
	if rec == nil {
		entry.mu.Unlock()
		return Interaction{}, fmt.Errorf("%w: %q for job %q", ErrUnknownInteraction, interactionID, jobID)
	}
	if rec.data.Status != InteractionPending {
		st := rec.data.Status
		entry.mu.Unlock()
		return Interaction{}, fmt.Errorf("%w: interaction %q (%s)", ErrInteractionState, interactionID, st)
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

	// Persist the answered snapshot first; then, if we resumed, the running job
	// snapshot — both into the SQLite metadata store.
	_ = s.meta.UpsertInteraction(toInteractionRecord(out))
	if resumeSnap != nil {
		_ = s.persist(*resumeSnap)
	}
	// E13: record the interaction.answered lifecycle event after persistence.
	s.recordEvent(jobID, EventInteractionAnswered, map[string]any{
		"interaction_id": out.ID,
		"answer":         out.Answer,
	})
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
		return Interaction{}, fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}

	entry.mu.Lock()
	rec := findInteraction(entry.interactions, interactionID)
	if rec == nil {
		entry.mu.Unlock()
		return Interaction{}, fmt.Errorf("%w: %q for job %q", ErrUnknownInteraction, interactionID, jobID)
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

// notLiveErr is the error for an interaction op (create/answer/inject) on a job
// not tracked in memory. It consults the metadata store so the result is
// deterministic: a job present in the DB but absent from memory was evicted
// after finishing (SP3), so it is terminal (ErrJobTerminal — no live agent);
// otherwise it is genuinely unknown (ErrUnknownJob). Without this DB check the
// in-memory terminal guard would race finish()'s eviction and report 409 or 404
// unpredictably for the same finished job.
func (s *Service) notLiveErr(jobID, action string) error {
	if _, ok, _ := s.meta.GetJob(jobID); ok {
		return fmt.Errorf("%w: job %q: cannot %s interaction", ErrJobTerminal, jobID, action)
	}
	return fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
}
