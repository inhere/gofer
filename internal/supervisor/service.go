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
	EscalateTo       string // "role:supervisor" | a concrete agent_id (default role:supervisor)
	MaxRoundsPerJob  int
	AllowPromptRegex []string
}

// action is the decision for one pending interaction.
type action int

const (
	actionEscalate action = iota
	actionAutoAnswer
)

// Service is the layered answerer. nowFn is injectable for tests; the dedup/round
// state is guarded by mu.
type Service struct {
	jobs     JobOps
	presence PresenceOps
	policy   Policy
	patterns []*regexp.Regexp
	nowFn    func() time.Time

	mu        sync.Mutex
	escalated map[string]bool // interaction ids already escalated (dedup)
	rounds    map[string]int  // job id -> interactions handled (auto+escalate)
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
		policy.EscalateTo = "role:supervisor"
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
		jobs:      jobs,
		presence:  presence,
		policy:    policy,
		patterns:  pats,
		nowFn:     time.Now,
		escalated: map[string]bool{},
		rounds:    map[string]int{},
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
		s.mu.Lock()
		alreadyEscalated := s.escalated[it.ID]
		s.mu.Unlock()
		if alreadyEscalated {
			continue // already routed to a human; awaiting their answer
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

// escalate posts an escalation to the configured inbox and marks the interaction so
// it is not re-posted on later ticks (dedup). Post failures are logged and NOT
// marked, so a transient failure retries next tick.
func (s *Service) escalate(it job.Interaction) {
	ref := "job:" + it.JobID + "#" + it.ID
	if _, err := s.presence.Post(systemFrom, s.policy.EscalateTo, escalateKind, it.Prompt, ref); err != nil {
		slog.Warn("supervisor: escalate post failed", "job_id", it.JobID, "interaction_id", it.ID, "err", err)
		return
	}
	s.mu.Lock()
	s.escalated[it.ID] = true
	s.rounds[it.JobID]++
	s.mu.Unlock()
	slog.Info("supervisor: escalated", "job_id", it.JobID, "interaction_id", it.ID, "to", s.policy.EscalateTo, "ref", ref)
}
