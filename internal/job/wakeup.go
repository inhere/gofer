package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/util"
)

// JOB-09 wakeups (design §五): an agent (or a human) registers an EVENT
// SUBSCRIPTION or a TIMER on a job and lets the job finish. When the condition
// arrives gofer starts ONE continuation of that job (`job resume`, or a rebuild
// with the instruction appended when there is no session to continue) and hands it
// the instruction written at registration time. Nothing stays resident: whether
// the work is actually done is judged by the continued agent itself.
//
// The vocabulary, validation, permission check and the fire path live here in the
// Service (G021); HTTP/CLI/MCP only bind + forward. The timer SCAN lives in serve's
// schedule tick (one sweeper for both, design §五.2) and the fire itself is
// triggered from the event observer installed in NewService (see onWakeupEvent).

// ErrWakeupForbidden marks a wakeup request from a caller that may not act on the
// target job (design §五.1 权限): the job's own caller, or a caller holding the
// answer capability, may. HTTP: 403.
var ErrWakeupForbidden = errors.New("caller may not wake this job")

// Wakeup claim/coalesce bookkeeping.
const (
	// wakeupMinEverySec is the floor for `--kind every`: a sub-minute timer is a
	// busy loop wearing a schedule's clothes, and cron covers the minute case.
	wakeupMinEverySec = 60
	// wakeupTerminalWait bounds how long a fire waits for its target job to reach a
	// terminal state before giving up (see dispatchWakeup).
	wakeupTerminalWait = 30 * time.Second
	// wakeupPromptMarker prefixes the instruction appended to a rebuilt prompt, so
	// the continued agent can tell its own earlier task from the wakeup's.
	wakeupPromptMarker = "[gofer wakeup] "
	// wakeupTagPrefix is the tag every continuation carries, so a job list answers
	// "which jobs ran because a wakeup fired".
	wakeupTagPrefix = "wakeup:"
)

// WakeupKinds lists the four registration shapes in documentation order.
var WakeupKinds = []string{
	jobstore.WakeupKindAt, jobstore.WakeupKindEvery,
	jobstore.WakeupKindCron, jobstore.WakeupKindEvent,
}

// wakeupEventCatalog is the v1 vocabulary of event types a wakeup may subscribe to
// (design §五.1). A type outside it is refused at CreateWakeup: subscribing to a
// typo would otherwise register a wakeup that can never fire.
var wakeupEventCatalog = map[string]bool{
	EventJobTerminal:             true,
	EventJobVerifyFinished:       true,
	EventJobNeedsReview:          true,
	EventJobReviewed:             true,
	EventJobFellBack:             true,
	EventJobStalled:              true,
	EventInteractionAnswered:     true,
	EventSessionTakeoverReleased: true,
}

// WakeupEventTypes returns the v1 event catalog in a stable order (for error
// messages, the CLI's help text and the MCP tool description).
func WakeupEventTypes() []string {
	return []string{
		EventJobTerminal, EventJobVerifyFinished, EventJobNeedsReview,
		EventJobReviewed, EventJobFellBack, EventJobStalled,
		EventInteractionAnswered, EventSessionTakeoverReleased,
	}
}

// WakeupSpec is the (client-settable) shape of one wakeup registration. It is the
// JSON body of POST /v1/jobs/{id}/wakeups, the CLI's flag set and the MCP tool's
// arguments — one type, so the three surfaces cannot drift.
//
// Every instant is unix SECONDS (the wire/time convention of this project): the
// CLI turns `--after 10m` / `--at <RFC3339>` into an absolute At, so the server
// never has to guess a client's clock offset from a duration.
type WakeupSpec struct {
	// JobID is the job the wakeup is registered on: the job a fire CONTINUES. It is
	// taken from the request path (HTTP) / the CLI's <job> argument and is ignored
	// when the caller passes one in the body.
	JobID string `json:"job_id,omitempty"`
	// Kind is one of jobstore.WakeupKind* (required).
	Kind string `json:"kind"`
	// At is the absolute fire instant for kind=at; it must be in the future.
	At int64 `json:"at,omitempty"`
	// EverySec is the repeat interval for kind=every (>= 60).
	EverySec int64 `json:"every_sec,omitempty"`
	// Cron / Timezone describe the schedule for kind=cron (a standard five-field
	// expression; an empty timezone means the server's local time).
	Cron     string `json:"cron,omitempty"`
	Timezone string `json:"timezone,omitempty"`
	// EventTypes are the subscribed event types for kind=event (catalog-checked).
	EventTypes []string `json:"event_types,omitempty"`
	// FilterJobID is the SOURCE job whose events are watched; empty means the job
	// the wakeup is registered on (the common case: "warn me when I finish").
	FilterJobID string `json:"filter_job_id,omitempty"`
	// FilterStatus narrows kind=event + job.terminal to these terminal statuses
	// (done/failed/cancelled/rejected); empty means every terminal status.
	FilterStatus []string `json:"filter_status,omitempty"`
	// Mode is jobstore.WakeupModeOnce / Continuous; empty defaults to once for
	// at/event and continuous for every/cron.
	Mode string `json:"mode,omitempty"`
	// Instruction is the prompt the continuation receives (design §五.1).
	Instruction string `json:"instruction,omitempty"`
	// ExpiresAt overrides the configured TTL (0 = now + wakeup.ttl_sec).
	ExpiresAt int64 `json:"expires_at,omitempty"`
}

// CreateWakeup validates and stores a wakeup on spec.JobID (design §五.1). `by` is
// the authenticated caller the HTTP/CLI/MCP entry stamped; it must be allowed to
// resume the target job and becomes the continuation's CallerID.
func (s *Service) CreateWakeup(spec WakeupSpec, by string) (jobstore.WakeupRecord, error) {
	src, ok := s.Get(spec.JobID)
	if !ok {
		return jobstore.WakeupRecord{}, fmt.Errorf("%w: %q", ErrUnknownJob, spec.JobID)
	}
	if !s.canWakeJob(src, by) {
		return jobstore.WakeupRecord{}, fmt.Errorf("%w: job %q belongs to caller %q", ErrWakeupForbidden, spec.JobID, src.CallerID)
	}
	w, err := s.buildWakeup(spec, src, by, s.nowFn())
	if err != nil {
		return jobstore.WakeupRecord{}, err
	}
	if err := s.meta.InsertWakeup(w); err != nil {
		return jobstore.WakeupRecord{}, err
	}
	return w, nil
}

// buildWakeup normalizes and validates a registration into a storable row.
func (s *Service) buildWakeup(spec WakeupSpec, src JobResult, by string, now time.Time) (jobstore.WakeupRecord, error) {
	w := jobstore.WakeupRecord{
		ID:             "wk-" + RandomSuffix(),
		JobID:          src.ID,
		Kind:           strings.TrimSpace(spec.Kind),
		At:             spec.At,
		EverySec:       spec.EverySec,
		CronExpr:       strings.TrimSpace(spec.Cron),
		Timezone:       strings.TrimSpace(spec.Timezone),
		FilterJobID:    strings.TrimSpace(spec.FilterJobID),
		Mode:           strings.TrimSpace(spec.Mode),
		Instruction:    strings.TrimSpace(spec.Instruction),
		Enabled:        1,
		Revision:       1,
		CreatedBy:      by,
		CreatedAt:      now.Unix(),
		EventTypesJSON: jobstore.EncodeStringList(spec.EventTypes),
	}
	if w.FilterJobID == "" {
		// 缺省 = 自己 (design §五.1): the "tell me when I am done" case, which is what
		// an agent registering from inside its own job means by default.
		w.FilterJobID = src.ID
	}
	if !contains(WakeupKinds, w.Kind) {
		return jobstore.WakeupRecord{}, fmt.Errorf("%w: kind must be one of %s", ErrInvalidRequest, strings.Join(WakeupKinds, "|"))
	}
	if w.Mode == "" {
		w.Mode = jobstore.WakeupModeOnce
		if w.Kind == jobstore.WakeupKindEvery || w.Kind == jobstore.WakeupKindCron {
			w.Mode = jobstore.WakeupModeContinuous
		}
	}
	if w.Mode != jobstore.WakeupModeOnce && w.Mode != jobstore.WakeupModeContinuous {
		return jobstore.WakeupRecord{}, fmt.Errorf("%w: mode must be %s|%s", ErrInvalidRequest, jobstore.WakeupModeOnce, jobstore.WakeupModeContinuous)
	}

	switch w.Kind {
	case jobstore.WakeupKindAt:
		if w.At <= now.Unix() {
			return jobstore.WakeupRecord{}, fmt.Errorf("%w: at must be in the future", ErrInvalidRequest)
		}
		w.NextRunAt = w.At
	case jobstore.WakeupKindEvery:
		if w.EverySec < wakeupMinEverySec {
			return jobstore.WakeupRecord{}, fmt.Errorf("%w: every must be at least %ds", ErrInvalidRequest, wakeupMinEverySec)
		}
		w.NextRunAt = now.Unix() + w.EverySec
	case jobstore.WakeupKindCron:
		next, err := wakeupCronNext(w.CronExpr, w.Timezone, now)
		if err != nil {
			return jobstore.WakeupRecord{}, err
		}
		w.NextRunAt = next
	case jobstore.WakeupKindEvent:
		types := jobstore.DecodeStringList(w.EventTypesJSON)
		if len(types) == 0 {
			return jobstore.WakeupRecord{}, fmt.Errorf("%w: kind=event requires --event", ErrInvalidRequest)
		}
		for _, t := range types {
			if !wakeupEventCatalog[t] {
				return jobstore.WakeupRecord{}, fmt.Errorf("%w: unknown event type %q (want one of %s)",
					ErrInvalidRequest, t, strings.Join(WakeupEventTypes(), ","))
			}
		}
		if len(spec.FilterStatus) > 0 {
			// The status filter is a job.terminal predicate only: any other event has
			// no status to match, so accepting it here would silently never fire.
			if !contains(types, EventJobTerminal) {
				return jobstore.WakeupRecord{}, fmt.Errorf("%w: --status applies to %s only", ErrInvalidRequest, EventJobTerminal)
			}
			for _, st := range spec.FilterStatus {
				if !contains(terminalStatuses, st) {
					return jobstore.WakeupRecord{}, fmt.Errorf("%w: unknown status %q", ErrInvalidRequest, st)
				}
			}
			w.FilterStatusJSON = jobstore.EncodeStringList(spec.FilterStatus)
		}
	}

	w.ExpiresAt = spec.ExpiresAt
	if w.ExpiresAt <= 0 {
		w.ExpiresAt = now.Add(s.config().WakeupTTL()).Unix()
	}
	return w, nil
}

// terminalStatuses is the set a job may END in — the values --status accepts.
var terminalStatuses = []string{
	StatusDone, StatusFailed, StatusCancelled, StatusRejected, StatusTimeout,
}

// canWakeJob reports whether callerID may register/act on wakeups for job `res`
// (design §五.1 权限: "must be able to `job resume` the job"). The precedent is the
// review/attach gate: the job's own caller always may; otherwise the caller needs
// the answer capability — but only when governance actually enforces it, so a
// deployment that never turned that gate on is not suddenly locked out of its own
// jobs.
func (s *Service) canWakeJob(res JobResult, callerID string) bool {
	if res.CallerID != "" && callerID == res.CallerID {
		return true
	}
	cfg := s.config()
	if cfg == nil || !cfg.Server.Governance.RequireAnswerCapability {
		return true
	}
	return cfg.Server.CallerCanAnswer(callerID)
}

// ListWakeups returns a job's wakeups (oldest first). Reads are not gated: a
// wakeup is job metadata, exactly like the job's events or deliveries.
func (s *Service) ListWakeups(jobID string) ([]jobstore.WakeupRecord, error) {
	return s.meta.ListWakeups(jobID)
}

// GetWakeup returns one wakeup row; ok=false when the id is unknown. Reads are not
// gated (see ListWakeups).
func (s *Service) GetWakeup(id string) (jobstore.WakeupRecord, bool, error) {
	return s.meta.GetWakeup(id)
}

// SetWakeupEnabled flips a wakeup's switch (`job wakeup enable|disable`). Enabling
// a TIMER re-arms it from now (design §五.1: every 从创建/启用时起算, 不补发), so a
// wakeup that was off for a week does not fire a week of missed ticks; enabling an
// `at` keeps its instant (an already-past one fires on the next sweep — the
// operator asked for it again).
func (s *Service) SetWakeupEnabled(id, by string, enabled bool) (jobstore.WakeupRecord, error) {
	w, err := s.mustWakeup(id)
	if err != nil {
		return jobstore.WakeupRecord{}, err
	}
	if err := s.authorizeWakeup(w, by); err != nil {
		return jobstore.WakeupRecord{}, err
	}
	if (w.Enabled == 1) == enabled {
		return w, nil // already in the requested state
	}
	if !enabled {
		if err := s.meta.SetWakeupEnabled(id, 0); err != nil {
			return jobstore.WakeupRecord{}, err
		}
		w.Enabled = 0
		return w, nil
	}
	now := s.nowFn()
	w.Enabled = 1
	switch w.Kind {
	case jobstore.WakeupKindEvery:
		w.NextRunAt = now.Unix() + w.EverySec
	case jobstore.WakeupKindCron:
		next, err := wakeupCronNext(w.CronExpr, w.Timezone, now)
		if err != nil {
			return jobstore.WakeupRecord{}, err
		}
		w.NextRunAt = next
	case jobstore.WakeupKindAt:
		w.NextRunAt = w.At
	default: // event: no timer to arm
		w.NextRunAt = 0
	}
	if err := s.meta.UpdateWakeup(w); err != nil {
		return jobstore.WakeupRecord{}, err
	}
	w.Revision++
	return w, nil
}

// DeleteWakeup removes a wakeup (`job wakeup rm`).
func (s *Service) DeleteWakeup(id, by string) error {
	w, err := s.mustWakeup(id)
	if err != nil {
		return err
	}
	if err := s.authorizeWakeup(w, by); err != nil {
		return err
	}
	return s.meta.DeleteWakeup(id)
}

// ExpireWakeup disables a wakeup whose TTL has passed and records job.wakeup_expired
// on the target job. The schedule sweep calls it for every expired row it finds
// (design §五.1 到期).
func (s *Service) ExpireWakeup(id string) {
	w, ok, err := s.meta.GetWakeup(id)
	if err != nil {
		slog.Warn("wakeup expire: get", "wakeup_id", id, "err", err)
		return
	}
	if !ok || w.Enabled != 1 {
		return
	}
	if err := s.meta.SetWakeupEnabled(id, 0); err != nil {
		slog.Warn("wakeup expire: disable", "wakeup_id", id, "err", err)
		return
	}
	s.recordEvent(w.JobID, EventJobWakeupExpired, map[string]any{"wakeup_id": w.ID, "kind": w.Kind})
}

// mustWakeup loads a wakeup or reports the standard "unknown" (404) error.
func (s *Service) mustWakeup(id string) (jobstore.WakeupRecord, error) {
	w, ok, err := s.meta.GetWakeup(id)
	if err != nil {
		return jobstore.WakeupRecord{}, err
	}
	if !ok {
		return jobstore.WakeupRecord{}, fmt.Errorf("%w: wakeup %q", ErrUnknownJob, id)
	}
	return w, nil
}

// authorizeWakeup applies canWakeJob to the job a wakeup belongs to.
func (s *Service) authorizeWakeup(w jobstore.WakeupRecord, by string) error {
	src, ok := s.Get(w.JobID)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownJob, w.JobID)
	}
	if !s.canWakeJob(src, by) {
		return fmt.Errorf("%w: job %q belongs to caller %q", ErrWakeupForbidden, w.JobID, src.CallerID)
	}
	return nil
}

// WakeupNextRun returns the next fire instant (unix seconds) for a timer wakeup
// strictly after afterUnix, or 0 for a wakeup that does not repeat (`at`, and every
// event subscription). An `every` timer counts from the sweep instant — a missed
// tick is never replayed (design §五.1) — and a cron expression is evaluated in the
// wakeup's own timezone. The schedule sweep in serve is its only caller.
func WakeupNextRun(w jobstore.WakeupRecord, afterUnix int64) (int64, error) {
	after := time.Unix(afterUnix, 0)
	switch w.Kind {
	case jobstore.WakeupKindEvery:
		if w.EverySec < wakeupMinEverySec {
			return 0, fmt.Errorf("wakeup %s: every_sec %d is below %d", w.ID, w.EverySec, wakeupMinEverySec)
		}
		return after.Unix() + w.EverySec, nil
	case jobstore.WakeupKindCron:
		return wakeupCronNext(w.CronExpr, w.Timezone, after)
	default:
		return 0, nil
	}
}

// wakeupCronNext is the shared cron evaluation (creation, re-arm and advance).
func wakeupCronNext(expr, tz string, after time.Time) (int64, error) {
	loc := time.Local
	if tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			return 0, fmt.Errorf("%w: unknown timezone %q", ErrInvalidRequest, tz)
		}
		loc = l
	}
	next, err := jobstore.NextCronRun(expr, after.In(loc))
	if err != nil {
		return 0, fmt.Errorf("%w: %s", ErrInvalidRequest, err)
	}
	return next, nil
}

// dispatchWakeup fires a wakeup, deferring to a goroutine when the target job has
// not reached its terminal state yet.
//
// The deferral is required by the event path: finish records job.terminal a few
// lines BEFORE it flips the status (see its E13 note), so an event wakeup
// registered on the job that just ended would be refused as "source job is not in
// a terminal state" if it resumed inline. Waiting on a SEPARATE goroutine is safe
// — the observer runs inside that job's own goroutine — and the bounded wait
// covers a job that never becomes terminal (needs_review parks until a human
// rules), where the fire then reports why.
func (s *Service) dispatchWakeup(w jobstore.WakeupRecord, reason string) {
	if res, ok := s.Get(w.JobID); ok && IsTerminal(res.Status) {
		s.FireWakeup(w.ID, reason)
		return
	}
	go func() {
		if _, ok := s.WaitFor(w.JobID, wakeupTerminalWait); !ok {
			slog.Warn("wakeup: target job did not reach a terminal state",
				"wakeup_id", w.ID, "job_id", w.JobID, "reason", reason)
		}
		s.FireWakeup(w.ID, reason)
	}()
}

// FireWakeup runs one wakeup trigger (design §五.1). It is the single place a
// continuation is started, called by the event matcher and by the schedule sweep.
//
// Coalescing (决策 6): a wakeup has ONE continuation slot. A trigger that finds it
// held by an unfinished continuation only counts a coalesce; the slot is freed
// when that continuation reaches a terminal state, so a continuous timer keeps
// working across turns without ever stacking them.
func (s *Service) FireWakeup(id, reason string) {
	w, ok, err := s.meta.GetWakeup(id)
	if err != nil {
		slog.Warn("wakeup fire: get", "wakeup_id", id, "err", err)
		return
	}
	if !ok || w.Enabled != 1 {
		return
	}
	// A wakeup whose TTL passed is dead: disable it here rather than start work the
	// sweeper is about to retire (the sweep normally gets there first).
	if w.ExpiresAt > 0 && w.ExpiresAt <= s.nowFn().Unix() {
		s.ExpireWakeup(id)
		return
	}
	if w.ContinuationJobID != "" {
		if w.IsContinuationPending() {
			s.coalesceWakeup(w, reason)
			return
		}
		if j, ok := s.Get(w.ContinuationJobID); ok && !IsTerminal(j.Status) {
			s.coalesceWakeup(w, reason)
			return
		}
		// The previous continuation finished (or its row is gone): free the slot.
		if err := s.meta.ReleaseWakeupClaim(w.ID, w.ContinuationJobID); err != nil {
			slog.Warn("wakeup fire: release claim", "wakeup_id", w.ID, "err", err)
		}
	}
	claimed, err := s.meta.ClaimWakeupFire(w.ID, jobstore.WakeupClaimPending)
	if err != nil {
		slog.Warn("wakeup fire: claim", "wakeup_id", w.ID, "err", err)
		return
	}
	if !claimed {
		// Lost the slot to a concurrent trigger (the sweeper and an event landing at
		// the same moment): count it, exactly like an occupied slot.
		s.coalesceWakeup(w, reason)
		return
	}
	res, err := s.wakeContinuation(w)
	if err != nil {
		// Free the slot so a later trigger can retry, and say so on the job: a wakeup
		// that silently never resumes looks exactly like a healthy idle one.
		if rerr := s.meta.ReleaseWakeupClaim(w.ID, jobstore.WakeupClaimPending); rerr != nil {
			slog.Warn("wakeup fire: release failed claim", "wakeup_id", w.ID, "err", rerr)
		}
		slog.Warn("wakeup fire: continuation failed", "wakeup_id", w.ID, "job_id", w.JobID, "reason", reason, "err", err)
		s.recordEvent(w.JobID, EventJobWakeupFailed, map[string]any{
			"wakeup_id": w.ID, "kind": w.Kind, "reason": reason, "error": err.Error(),
		})
		return
	}
	if _, err := s.meta.SetWakeupContinuation(w.ID, res.ID); err != nil {
		slog.Warn("wakeup fire: record continuation", "wakeup_id", w.ID, "job_id", res.ID, "err", err)
	}
	s.recordEvent(w.JobID, EventJobWakeupFired, map[string]any{
		"wakeup_id": w.ID, "kind": w.Kind, "reason": reason, "continuation_job": res.ID,
	})
	if w.Mode == jobstore.WakeupModeOnce {
		if err := s.meta.SetWakeupEnabled(w.ID, 0); err != nil {
			slog.Warn("wakeup fire: consume", "wakeup_id", w.ID, "err", err)
		}
	}
}

// coalesceWakeup counts a trigger that did not start a continuation because the
// single slot was busy (design §五.1 coalesced_count).
func (s *Service) coalesceWakeup(w jobstore.WakeupRecord, reason string) {
	if err := s.meta.CoalesceWakeup(w.ID, s.nowFn().Unix()); err != nil {
		slog.Warn("wakeup coalesce", "wakeup_id", w.ID, "err", err)
		return
	}
	s.recordEvent(w.JobID, EventJobWakeupCoalesced, map[string]any{"wakeup_id": w.ID, "reason": reason})
}

// wakeContinuation starts the continuation a wakeup asked for: continue the target
// job's agent session when it has one, else rebuild the original request with the
// instruction appended to its prompt (design §五.1). The continuation carries the
// tag `wakeup:<id>` so the chain it belongs to is visible in any job listing.
func (s *Service) wakeContinuation(w jobstore.WakeupRecord) (JobResult, error) {
	tags := []string{wakeupTagPrefix + w.ID}
	res, err := s.resumeJob(w.JobID, w.Instruction, "", w.CreatedBy, 0, tags)
	if err == nil {
		return res, nil
	}
	if !errors.Is(err, ErrNoSession) && !errors.Is(err, ErrResumeUnsupported) {
		// Anything else (unknown/ non-terminal / cross-runner / submit refused) is a
		// real failure the operator must see: rebuild only stands in for "this job has
		// no session to continue".
		return JobResult{}, err
	}
	ov := RebuildOverrides{Tags: &tags}
	if src, ok := s.Get(w.JobID); ok {
		if p := wakeupPrompt(src, w.Instruction); p != "" {
			ov.Prompt = &p
		}
	}
	return s.RebuildJob(w.JobID, ov, w.CreatedBy, "")
}

// wakeupPrompt appends the wakeup's instruction to the source job's prompt (design
// §五.1: 无 session 时退化为重跑 + 追加指令). An empty instruction (or a source with
// no prompt at all — an exec job's argv IS its task) yields "" and the rebuild
// inherits the original prompt verbatim: the instruction then lives only in the
// job.wakeup_fired event.
func wakeupPrompt(src JobResult, instruction string) string {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return ""
	}
	orig := promptFromRequestJSON(src.RequestJSON)
	if strings.TrimSpace(orig) == "" {
		return wakeupPromptMarker + instruction
	}
	return orig + "\n\n" + wakeupPromptMarker + instruction
}

// promptFromRequestJSON recovers the prompt a job was submitted with (mirrors
// cwdFromRequestJSON: request_json is the only place the ORIGINAL prompt survives).
func promptFromRequestJSON(blob string) string {
	if blob == "" {
		return ""
	}
	var r struct {
		Prompt string `json:"prompt"`
	}
	_ = json.Unmarshal([]byte(blob), &r)
	return r.Prompt
}

// onWakeupEvent is the in-process event observer (installed in NewService) that
// turns a recorded lifecycle event into a fire. Registration on a source job the
// caller does not own is refused at CreateWakeup, so matching here is pure
// filtering: kind=event, enabled, the subscribed type, the source job and — for
// job.terminal — the status filter.
func (s *Service) onWakeupEvent(jobID, eventType string, detail map[string]any) {
	cands, err := s.meta.MatchingEventWakeups(jobID, eventType)
	if err != nil {
		slog.Warn("wakeup matcher: query", "job_id", jobID, "type", eventType, "err", err)
		return
	}
	for _, w := range cands {
		if !wakeupStatusMatches(w, eventType, detail) {
			continue
		}
		// A wakeup never triggers itself: the events its own continuation produces
		// must not feed back into it (design §五.1).
		if jobID != "" && w.ContinuationJobID == jobID {
			continue
		}
		s.dispatchWakeup(w, eventType)
	}
}

// wakeupStatusMatches applies the optional status filter of a job.terminal
// subscription. Every other event carries no status, so the filter is a no-op for
// them (CreateWakeup refuses such a combination in the first place).
func wakeupStatusMatches(w jobstore.WakeupRecord, eventType string, detail map[string]any) bool {
	want := jobstore.DecodeStringList(w.FilterStatusJSON)
	if len(want) == 0 || eventType != EventJobTerminal || detail == nil {
		return len(want) == 0
	}
	got, _ := detail["status"].(string)
	return contains(want, got)
}

// contains is the tiny membership test this file needs (the slices are a handful of
// literal strings, so a linear scan beats building a set).
func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// wakeupTagList appends extra tags to a source job's tags, so a continuation keeps
// its provenance AND carries its reason. Capacity through util.CapSum (G041).
func wakeupTagList(src, extra []string) []string {
	if len(extra) == 0 {
		return src
	}
	out := make([]string, 0, util.CapSum(len(src), len(extra)))
	out = append(out, src...)
	return append(out, extra...)
}
