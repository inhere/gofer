package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	// ErrAnswerNotAllowed — the派生作答白名单闸 (P3.1) refused an attributed driver answer
	// (a通用 sup answering outside the whitelist). The interaction stays pending (escalates
	// to a human). HTTP: 403.
	ErrAnswerNotAllowed = errors.New("answer not allowed")
)

// answered_by source tags (监督分层升级路由 P3.2, design §10). The answer path stamps one
// onto interactions.answered_by so an audit can tell apart L0/L1·L2/L3 应答来源.
const (
	// answeredByHuman — L3: a web/CLI answer with no driver identity.
	answeredByHuman = "human"
	// answeredByAgentPrefix — L1 owner / L2 sup: an attributed driver answer; the agent_id
	// follows (agent:<id>). owner vs sup are told apart by which agent_id it is.
	answeredByAgentPrefix = "agent:"
	// answeredByAutoPrefix — L0: the built-in rule answerer; the policy name follows
	// (auto:<policy>, e.g. auto:choice).
	answeredByAutoPrefix = "auto:"
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
	// EscalatedAt 是该 interaction 被 escalate（投递给上层应答者）的 unix 秒时间戳（监督
	// 分层升级路由 P1.1, design §9）：承载 escalate dedup 标记 + owner 超时计时。0 表示尚未
	// escalate。P1.1 仅落库 + 透传读出，写入由 P1.2（escalate dedup）/P2.1（超时）落地。
	EscalatedAt int64 `json:"escalated_at,omitempty"`
	// AnsweredBy 记录"谁应答了该 interaction"（监督分层升级路由 P3.2, design §10 审计区分）：
	// auto:<policy>(L0 内置规则器) / agent:<id>(L1 owner / L2 sup) / human(L3 web/CLI)。
	// "" 表示尚未应答或未归因（内部 relay）。gofer_get_interactions / 查询接口可见。
	AnsweredBy string `json:"answered_by,omitempty"`
	// NeedsHuman 标记该 interaction 已被通用 sup 判为高危/拿不准、显式留给人处理（事件驱动按需
	// 派发 y5wt）：1=留给人。把它排除出 CountSupPendingDemand 的 sup demand，避免反复唤醒 sup
	// 去重新拒答同一条。0=未标记。interaction 仍 pending（待人应答）。
	NeedsHuman int64 `json:"needs_human,omitempty"`
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
	// interaction row, both into the SQLite metadata store (best-effort —
	// 失败不阻断创建，但记 warning，避免 DB 与内存态静默漂移)。
	if err := s.persist(snap); err != nil {
		slog.Warn("persist pending-interaction snapshot", "job_id", jobID, "err", err)
	}
	if err := s.meta.UpsertInteraction(toInteractionRecord(out)); err != nil {
		slog.Warn("upsert created interaction", "job_id", jobID, "interaction_id", out.ID, "err", err)
	}

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
		EscalatedAt: it.EscalatedAt,
		AnsweredBy:  it.AnsweredBy,
		NeedsHuman:  it.NeedsHuman,
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
		ID:          rec.ID,
		JobID:       rec.JobID,
		Type:        rec.Type,
		Prompt:      rec.Prompt,
		Options:     opts,
		Status:      rec.Status,
		Answer:      rec.Answer,
		CreatedAt:   rec.CreatedAt,
		AnsweredAt:  rec.AnsweredAt,
		EscalatedAt: rec.EscalatedAt,
		AnsweredBy:  rec.AnsweredBy,
		NeedsHuman:  rec.NeedsHuman,
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

// ListPendingInteractions returns the pending interactions across all ACTIVE jobs
// (E25 监督发现): the data-plane read straight from the metadata store, filtered to
// non-terminal jobs (jobstore JOINs jobs). Each carries its job_id so the caller
// can route an answer/escalation. It reads the DB (not the in-memory entries) so it
// is consistent across the whole serve, not just jobs live in this process.
func (s *Service) ListPendingInteractions() ([]Interaction, error) {
	recs, err := s.meta.ListPendingInteractions()
	if err != nil {
		return nil, err
	}
	out := make([]Interaction, 0, len(recs))
	for _, rec := range recs {
		out = append(out, fromInteractionRecord(rec))
	}
	return out, nil
}

// ReconcileOrphanInteractions flips pending interactions of already-terminal jobs
// to cancelled (crash-recovery backstop for finish()'s in-memory reconciliation,
// 复审 #4). Returns the rows fixed. Serve calls it once at startup.
func (s *Service) ReconcileOrphanInteractions() (int, error) {
	return s.meta.ReconcileOrphanInteractions(s.nowFn().Unix())
}

// ReconcileOrphanJobs fails every job left non-terminal (queued/running) in the
// store by a serve that died / restarted mid-flight — their in-memory orchestration
// (dispatch entry / worker sink) did not survive, so a worker that restarted (hub
// supersede, §5.5) or kept running has nowhere to report back and the job would hang
// "running" forever. Mirrors ReconcileOrphanInteractions; serve calls it once at
// startup, before new work is accepted (the in-memory map is empty, so no live job
// is touched). Returns the rows fixed.
func (s *Service) ReconcileOrphanJobs() (int, error) {
	return s.meta.ReconcileOrphanJobs(s.nowFn().Unix(), "orphaned: serve restarted while job was non-terminal")
}

// MarkInteractionEscalated stamps escalated_at on a pending interaction — the
// supervisor's escalate dedup + owner-timeout clock (design §8.1/§8.2). It updates the
// live in-memory rec when the job is still tracked here (so a later AnswerInteraction
// upsert preserves the stamp instead of resetting it to 0), and always writes the DB
// row so the cross-process / cross-restart dedup read (ListPendingInteractions) sees
// it. ts is the escalate moment (unix seconds). Best-effort persistence is the
// caller's concern; this surfaces the store error so the supervisor can log it.
func (s *Service) MarkInteractionEscalated(jobID, interactionID string, ts int64) error {
	if entry := s.entry(jobID); entry != nil {
		entry.mu.Lock()
		if rec := findInteraction(entry.interactions, interactionID); rec != nil {
			rec.data.EscalatedAt = ts
		}
		entry.mu.Unlock()
	}
	return s.meta.MarkInteractionEscalated(jobID, interactionID, ts)
}

// MarkInteractionNeedsHuman flags a pending interaction as "punted to a human" by the通用
// sup (高危/拿不准, 事件驱动按需派发 y5wt). Like MarkInteractionEscalated it updates the live
// in-memory rec when the job is still tracked here (so a later upsert preserves the flag
// instead of resetting it to 0), and always writes the DB row so the cross-process demand
// read (CountSupPendingDemand) sees it and stops re-waking a sup for the same interaction.
// The interaction stays pending — a human answers it via web/CLI later.
func (s *Service) MarkInteractionNeedsHuman(jobID, interactionID string) error {
	if entry := s.entry(jobID); entry != nil {
		entry.mu.Lock()
		if rec := findInteraction(entry.interactions, interactionID); rec != nil {
			rec.data.NeedsHuman = 1
		}
		entry.mu.Unlock()
	}
	return s.meta.MarkInteractionNeedsHuman(jobID, interactionID)
}

// MarkInteractionNeedsHumanBy marks an interaction as needing a human and records
// the authenticated caller that punted it. The metadata update remains the same
// operation as MarkInteractionNeedsHuman so MCP/agent callers can keep using the
// original unattributed path.
func (s *Service) MarkInteractionNeedsHumanBy(jobID, interactionID, callerID string) error {
	if err := s.MarkInteractionNeedsHuman(jobID, interactionID); err != nil {
		return err
	}
	if _, ok := s.interactionSnapshot(jobID, interactionID); !ok {
		return nil
	}
	s.recordEvent(jobID, EventInteractionPunted, map[string]any{
		"interaction_id": interactionID,
		"caller_id":      callerID,
	})
	return nil
}

// AnswerInteraction marks a pending interaction answered WITHOUT attribution — the
// internal / relay path (worker→local resume, peer-http relay) where the answer was already
// decided upstream, so answered_by stays "". For an ATTRIBUTED answer (driver / web / CLI)
// use AnswerInteractionBy; for the L0 rule answerer use AnswerInteractionAuto.
func (s *Service) AnswerInteraction(jobID, interactionID, answer string) (Interaction, error) {
	return s.answerInteraction(jobID, interactionID, answer, "")
}

// AnswerInteractionBy answers an interaction ATTRIBUTED to responder (a driver agent_id;
// "" = human web/CLI). It is the gated entry (监督分层升级路由 P3.1, design §8.5): when a
// guard is wired AND responder is non-empty, the派生作答白名单闸 grades the source — owner /
// human放行, a通用 sup is held to the whitelist (choice + options + allow_prompt_regex);
// outside it the answer is REFUSED (ErrAnswerNotAllowed) and the interaction stays pending
// (escalates to a human). answered_by is stamped human (responder=="") or agent:<responder>.
func (s *Service) AnswerInteractionBy(jobID, interactionID, answer, responder string) (Interaction, error) {
	if responder != "" && s.answerGuard != nil {
		jr, _ := s.Get(jobID) // owner column (origin_agent) for source grading
		if it, ok := s.interactionSnapshot(jobID, interactionID); ok {
			if err := s.answerGuard.Check(responder, jr.OriginAgent, it.Type, len(it.Options) > 0, it.Prompt); err != nil {
				return Interaction{}, fmt.Errorf("%w: %s", ErrAnswerNotAllowed, err.Error())
			}
		}
	}
	by := answeredByHuman
	if responder != "" {
		by = answeredByAgentPrefix + responder
	}
	return s.answerInteraction(jobID, interactionID, answer, by)
}

// AnswerInteractionByHuman answers an interaction as a web/CLI human operator.
// It skips the derived-agent answer guard and stamps the server-authenticated
// caller id without an agent: prefix. Empty caller ids retain the legacy "human"
// attribution for allow_empty_token / anonymous deployments.
func (s *Service) AnswerInteractionByHuman(jobID, interactionID, answer, callerID string) (Interaction, error) {
	by := callerID
	if by == "" {
		by = answeredByHuman
	}
	return s.answerInteraction(jobID, interactionID, answer, by)
}

// AnswerInteractionAuto answers an interaction as the L0 built-in rule answerer
// (监督分层升级路由 P3.2). It is NEVER gated (the rule answerer has its own decide() gate)
// and stamps answered_by=auto:<policy> (e.g. auto:choice). policy names the rule that fired.
func (s *Service) AnswerInteractionAuto(jobID, interactionID, answer, policy string) (Interaction, error) {
	return s.answerInteraction(jobID, interactionID, answer, answeredByAutoPrefix+policy)
}

// interactionSnapshot returns the current snapshot of one interaction (live or persisted),
// or ok=false when the job/interaction is unknown. Used by the gate to read the immutable
// type/prompt/options BEFORE the answer (no lock held; type/prompt/options never change after
// creation, so this read is race-free for gating).
func (s *Service) interactionSnapshot(jobID, interactionID string) (Interaction, bool) {
	list, err := s.GetInteractions(jobID)
	if err != nil {
		return Interaction{}, false
	}
	for _, it := range list {
		if it.ID == interactionID {
			return it, true
		}
	}
	return Interaction{}, false
}

// answerInteraction marks a pending interaction answered, records the answer + answered_by,
// wakes any waiter, and — when no pending interactions remain — flips the job status back to
// running (never overriding a terminal status). Errors if job/interaction unknown or the
// interaction is not pending. answeredBy ("" = unattributed) is stamped onto the row.
func (s *Service) answerInteraction(jobID, interactionID, answer, answeredBy string) (Interaction, error) {
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
	rec.data.AnsweredBy = answeredBy
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
	// snapshot — both into the SQLite metadata store (best-effort，失败记 warning)。
	if err := s.meta.UpsertInteraction(toInteractionRecord(out)); err != nil {
		slog.Warn("upsert answered interaction", "job_id", jobID, "interaction_id", out.ID, "err", err)
	}
	if resumeSnap != nil {
		if err := s.persist(*resumeSnap); err != nil {
			slog.Warn("persist resumed-running snapshot", "job_id", jobID, "err", err)
		}
	}
	// E13: record the interaction.answered lifecycle event after persistence.
	s.recordEvent(jobID, EventInteractionAnswered, map[string]any{
		"interaction_id": out.ID,
		"answer":         out.Answer,
		"answered_by":    out.AnsweredBy,
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
