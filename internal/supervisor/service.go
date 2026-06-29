// Package supervisor is the E25 layered answerer (design §8.3-8.4). It polls for
// pending job interactions and either auto-answers the narrow low-risk slice
// (choice + enumerable options + whitelisted prompt) or escalates everything else
// (confirmation / free-text question / no-options / over-rounds / non-whitelisted)
// to a human via the E36 mailbox.
//
// Honest scope (design §11): auto-answer is deliberately收窄 — its coverage is low
// by design; the real value is reliable ROUTING (discover pending → escalate to the
// right inbox), not autonomously answering. Escalation is the default; auto-answer
// only fires when explicitly opted into (AutoAnswer=true AND a prompt matches the
// whitelist).
//
// Layering (G022): supervisor consumes job + presence via the narrow JobOps /
// PresenceOps interfaces it defines here (dependency inversion, mirroring job's
// WorkflowAdvancer). job and presence never import supervisor.
package supervisor

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/job"
)

// JobOps is the narrow job-service capability the supervisor needs. job.Service
// satisfies it. ErrJobTerminal (a zombie interaction whose job已终态) is surfaced so
// tick can skip it silently (复审 #4).
type JobOps interface {
	ListPendingInteractions() ([]job.Interaction, error)
	AnswerInteraction(jobID, interactionID, answer string) (job.Interaction, error)
	// Get returns the job snapshot (owner routing columns OriginAgent/EscalateTo) for
	// owner-first escalation (§8.1). The bool is false for an unknown id, in which case
	// the zero JobResult (empty owner cols) routes straight to the global policy.
	// Satisfied by job.Service.Get.
	Get(jobID string) (job.JobResult, bool)
	// MarkInteractionEscalated stamps interactions.escalated_at (escalate dedup +
	// owner-timeout clock, design §8.1/§9), replacing the old in-memory escalated map
	// so dedup survives a serve restart. Satisfied by job.Service.MarkInteractionEscalated.
	MarkInteractionEscalated(jobID, interactionID string, ts int64) error
}

// PresenceOps is the narrow mailbox capability used to escalate to a human/agent.
// presence.Service satisfies it.
type PresenceOps interface {
	Post(from, to, kind, body, ref string) (delivered int, err error)
}

// DefaultInterval / DefaultMaxRounds are the poller cadence and per-job
// auto-answer budget defaults (design §8.4 熔断).
const (
	DefaultInterval  = 5 * time.Second
	DefaultMaxRounds = 3

	// DefaultEscalateTo is the global escalation recipient when the policy leaves
	// escalate_to empty. role-one:<role> targets a SINGLE online supervisor (取一,
	// design §8.1) so multiple online sups don't race to answer the same interaction
	// (role: would fan out to ALL of them). Changed from role:supervisor in
	// supervisor-routing P1.2.
	DefaultEscalateTo = "role-one:supervisor"

	escalateKind = "escalation"
	systemFrom   = "system"
)

// Policy configures the answerer. The zero value is inert (Enabled=false). Empty
// AllowPromptRegex means NOTHING is whitelisted → every choice escalates too
// (auto-answer must be explicitly opted into via a matching pattern).
type Policy struct {
	Enabled          bool
	Interval         time.Duration
	AutoAnswer       bool
	EscalateTo       string // a to-spec ("role-one:supervisor" | "role:..." | a concrete agent_id); default DefaultEscalateTo
	MaxRoundsPerJob  int
	AllowPromptRegex []string
}

// action is the decision for one pending interaction.
type action int

const (
	actionEscalate action = iota
	actionAutoAnswer
)

// Service is the layered answerer. nowFn is injectable for tests; the per-job round
// budget (rounds) is guarded by mu. Escalate dedup is NO LONGER in-memory — it lives
// in interactions.escalated_at (design §9, P1.2), so it survives a serve restart.
type Service struct {
	jobs     JobOps
	presence PresenceOps
	policy   Policy
	patterns []*regexp.Regexp
	nowFn    func() time.Time

	mu     sync.Mutex
	rounds map[string]int // job id -> interactions handled (auto+escalate)
}

// NewService builds a Service, applying defaults for a zero Interval / MaxRounds /
// EscalateTo and compiling the prompt whitelist (an invalid pattern is dropped with
// a warning rather than failing construction).
func NewService(jobs JobOps, presence PresenceOps, policy Policy) *Service {
	if policy.Interval <= 0 {
		policy.Interval = DefaultInterval
	}
	if policy.MaxRoundsPerJob <= 0 {
		policy.MaxRoundsPerJob = DefaultMaxRounds
	}
	if policy.EscalateTo == "" {
		policy.EscalateTo = DefaultEscalateTo
	}
	var pats []*regexp.Regexp
	for _, p := range policy.AllowPromptRegex {
		re, err := regexp.Compile(p)
		if err != nil {
			slog.Warn("supervisor: invalid allow_prompt_regex, skipping", "pattern", p, "err", err)
			continue
		}
		pats = append(pats, re)
	}
	return &Service{
		jobs:     jobs,
		presence: presence,
		policy:   policy,
		patterns: pats,
		nowFn:    time.Now,
		rounds:   map[string]int{},
	}
}

// Run is the poller loop: it ticks once immediately then every Interval until ctx
// is cancelled (serve shutdown). A disabled policy returns at once (no goroutine
// churn). Tick errors are logged, never fatal.
func (s *Service) Run(ctx context.Context) {
	if !s.policy.Enabled {
		return
	}
	slog.Info("supervisor: answerer started", "interval", s.policy.Interval, "auto_answer", s.policy.AutoAnswer, "escalate_to", s.policy.EscalateTo)
	s.tick(ctx)
	ticker := time.NewTicker(s.policy.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// tick lists active pending interactions and decides+acts on each. It is exported
// to tests via the package (unexported here but driven directly in service_test).
func (s *Service) tick(_ context.Context) {
	list, err := s.jobs.ListPendingInteractions()
	if err != nil {
		slog.Warn("supervisor: list pending interactions failed", "err", err)
		return
	}
	for _, it := range list {
		// Dedup via the persisted interactions.escalated_at (replaces the in-memory
		// escalated map): a non-zero stamp means a prior tick — this process OR a
		// previous serve — already routed it to a human/owner, so skip until answered.
		if it.EscalatedAt > 0 {
			continue
		}
		switch s.decide(it) {
		case actionAutoAnswer:
			s.autoAnswer(it)
		default:
			s.escalate(it)
		}
	}
}

// decide classifies one interaction (design §8.3, honestly收窄). Escalate is the
// default; auto-answer only for choice + enumerable options + whitelisted prompt,
// when AutoAnswer is on and the per-job round budget is not spent.
func (s *Service) decide(it job.Interaction) action {
	s.mu.Lock()
	overBudget := s.rounds[it.JobID] >= s.policy.MaxRoundsPerJob
	s.mu.Unlock()
	if overBudget {
		return actionEscalate // 套娃/runaway 熔断
	}
	if !s.policy.AutoAnswer {
		return actionEscalate
	}
	switch it.Type {
	case job.InteractionTypeChoice:
		if len(it.Options) == 0 {
			return actionEscalate // can't enumerate a safe answer
		}
		if !s.matchWhitelist(it.Prompt) {
			return actionEscalate
		}
		return actionAutoAnswer
	default:
		// confirmation (E8 审批门) + free-text question + unknown: always escalate.
		return actionEscalate
	}
}

// matchWhitelist reports whether the prompt is allowed for auto-answer. With NO
// configured patterns nothing is whitelisted (conservative: opt-in only).
func (s *Service) matchWhitelist(prompt string) bool {
	if len(s.patterns) == 0 {
		return false
	}
	for _, re := range s.patterns {
		if re.MatchString(prompt) {
			return true
		}
	}
	return false
}

// autoAnswer answers a whitelisted choice with its first (default) option value. A
// zombie interaction (ErrJobTerminal) is skipped silently (复审 #4); other errors
// are logged and left for a later tick.
func (s *Service) autoAnswer(it job.Interaction) {
	answer := it.Options[0].Value
	if _, err := s.jobs.AnswerInteraction(it.JobID, it.ID, answer); err != nil {
		if errors.Is(err, job.ErrJobTerminal) {
			return // job already终态 (zombie); reconciliation will clean it up
		}
		slog.Warn("supervisor: auto-answer failed", "job_id", it.JobID, "interaction_id", it.ID, "err", err)
		return
	}
	s.mu.Lock()
	s.rounds[it.JobID]++
	s.mu.Unlock()
	slog.Info("supervisor: auto-answered", "job_id", it.JobID, "interaction_id", it.ID, "answer", answer, "answered_by", "auto:choice")
}

// escalate routes a pending interaction owner-first (design §8.1): it tries, in order,
//  1. the job's origin agent (owner) — direct-投 by its BARE agent_id, which presence
//     treats as store-and-forward (lands in the owner's inbox even if briefly offline);
//  2. an optional job-level escalate_to override;
//  3. the global policy default (DefaultEscalateTo = role-one:supervisor).
//
// The FIRST target that delivers >0 wins and stops the chain. On a delivery it stamps
// interactions.escalated_at (dedup, replacing the in-memory map) so later ticks / a
// serve restart don't re-post. No reachable recipient → leave it pending (no stamp): a
// later tick retries and ultimately a human picks it up (L3). A Post error is logged
// and the next target is tried.
func (s *Service) escalate(it job.Interaction) {
	jr, _ := s.jobs.Get(it.JobID) // zero JobResult when unknown → empty owner cols → policy-only
	ref := "job:" + it.JobID + "#" + it.ID

	targets := make([]string, 0, 3)
	if jr.OriginAgent != "" {
		targets = append(targets, jr.OriginAgent) // L1: owner (bare agent_id, store-and-forward)
	}
	if jr.EscalateTo != "" {
		targets = append(targets, jr.EscalateTo) // job-level override
	}
	targets = append(targets, s.policy.EscalateTo) // L2: global sup (default role-one:supervisor)

	deliveredTo := ""
	for _, to := range targets {
		n, err := s.presence.Post(systemFrom, to, escalateKind, it.Prompt, ref)
		if err != nil {
			slog.Warn("supervisor: escalate post failed", "job_id", it.JobID, "interaction_id", it.ID, "to", to, "err", err)
			continue
		}
		if n > 0 {
			deliveredTo = to
			break
		}
	}
	if deliveredTo == "" {
		// Nobody reachable (owner unknown/pruned AND no online sup): not stamped, so a
		// later tick retries; a human (L3) is the backstop.
		slog.Info("supervisor: escalate found no recipient", "job_id", it.JobID, "interaction_id", it.ID, "targets", targets)
		return
	}

	// dedup落表: stamp escalated_at so a later tick / a serve restart does not re-post.
	if err := s.jobs.MarkInteractionEscalated(it.JobID, it.ID, s.nowFn().Unix()); err != nil {
		// Non-fatal: dedup degrades to possibly re-posting next tick; the message was
		// already delivered, so do not undo the round accounting below.
		slog.Warn("supervisor: mark interaction escalated failed", "job_id", it.JobID, "interaction_id", it.ID, "err", err)
	}
	s.mu.Lock()
	s.rounds[it.JobID]++
	s.mu.Unlock()
	slog.Info("supervisor: escalated", "job_id", it.JobID, "interaction_id", it.ID, "to", deliveredTo, "ref", ref)
}
