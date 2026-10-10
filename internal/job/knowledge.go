package job

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/tracker"
)

// Knowledge candidates (gofer-3nxa.2, design 2026-10-10-handoff-brief-and-knowledge-loop
// §三): a job whose 「交付约定」 asked for it reports cross-task lessons in a
// 「## 可复用经验」 section; its finish turns the items into pending memory candidates,
// and a person accepts one (it becomes a scoped memory, source job:<id>) or rejects it.
// Nothing enters memory automatically.

// KnowledgeSectionTitle is the report section an agent lists reusable lessons in.
const KnowledgeSectionTitle = "可复用经验"

// knowledgeReportBytes is how much of the report's tail is parsed — the same window
// `job review` and the review panel read.
const knowledgeReportBytes = 64 * 1024

// ErrMemoryCandidateKeyExists is an accept whose key already names a live memory in
// the target scope: accepting never overwrites a memory.
var ErrMemoryCandidateKeyExists = errors.New("memory key already exists in that scope")

// ParseKnowledge extracts the items of the LAST 「## 可复用经验」 section of a report
// (same rules as ParseFindings, see ParseReportSection).
func ParseKnowledge(report string) []string {
	return ParseReportSection(report, KnowledgeSectionTitle)
}

// captureKnowledge records the 「## 可复用经验」 items of a delivered job as pending
// memory candidates. It runs in finish once the terminal (or needs_review) row is
// durable, only for a job Submit asked for the section (KnowledgeCapture — so a
// project with knowledge_capture off pays nothing), and only for a delivery (done /
// needs_review). Idempotent (one row per job + text) and best-effort: a failure is
// logged, never the job's problem.
func (s *Service) captureKnowledge(snap JobResult) {
	if !snap.KnowledgeCapture || (snap.Status != StatusDone && snap.Status != StatusNeedsReview) {
		return
	}
	report, err := s.TailLog(snap.ID, store.StreamStdout, knowledgeReportBytes)
	if err != nil {
		slog.Debug("knowledge capture: no report", "job_id", snap.ID, "err", err)
		return
	}
	items := ParseKnowledge(string(report))
	if len(items) == 0 {
		return
	}
	if _, err := s.meta.AddMemoryCandidates(snap.ID, snap.ProjectKey, items); err != nil {
		slog.Warn("knowledge capture: record candidates", "job_id", snap.ID, "err", err)
	}
}

// ListMemoryCandidates lists memory candidates (status "" = pending, "all" = every state).
func (s *Service) ListMemoryCandidates(f jobstore.MemoryCandidateFilter) ([]jobstore.MemoryCandidate, error) {
	return s.meta.ListMemoryCandidates(f)
}

// AcceptMemoryCandidateInput is how a person files a candidate as a memory. Key is
// required; Kind is rule | note (default note); the scope is the candidate's project
// unless Global or Project names another one.
type AcceptMemoryCandidateInput struct {
	Key     string `json:"key"`
	Kind    string `json:"kind,omitempty"`
	Summary string `json:"summary,omitempty"`
	Global  bool   `json:"global,omitempty"`
	Project string `json:"project,omitempty"`
}

// MemoryCandidateDecision is the outcome of accepting a candidate: the decided row and
// the memory it became.
type MemoryCandidateDecision struct {
	Candidate jobstore.MemoryCandidate `json:"candidate"`
	Memory    *jobstore.ScopedMemory   `json:"memory,omitempty"`
}

// AcceptMemoryCandidate writes candidate id as a scoped memory (content = the
// candidate's text, source = job:<id>) and marks it accepted. The candidate is claimed
// first (pending → accepted, a compare-and-set), so two concurrent accepts cannot both
// write; a failed memory write puts it back to pending. A key that already names a live
// memory in the target scope is refused (ErrMemoryCandidateKeyExists).
func (s *Service) AcceptMemoryCandidate(id int64, in AcceptMemoryCandidateInput, by string) (MemoryCandidateDecision, error) {
	key := strings.TrimSpace(in.Key)
	if key == "" {
		return MemoryCandidateDecision{}, fmt.Errorf("%w: key is required", ErrInvalidRequest)
	}
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = tracker.MemoryKindNote
	}
	if kind != tracker.MemoryKindRule && kind != tracker.MemoryKindNote {
		return MemoryCandidateDecision{}, fmt.Errorf("%w: kind must be rule or note", ErrInvalidRequest)
	}
	cand, err := s.meta.GetMemoryCandidate(id)
	if err != nil {
		return MemoryCandidateDecision{}, err
	}
	if cand.Status != jobstore.MemoryCandidatePending {
		return MemoryCandidateDecision{}, jobstore.ErrMemoryCandidateDecided
	}
	scope, scopeKey := jobstore.ScopedMemoryProject, strings.TrimSpace(in.Project)
	if in.Global {
		if scopeKey != "" {
			return MemoryCandidateDecision{}, fmt.Errorf("%w: global and project are mutually exclusive", ErrInvalidRequest)
		}
		scope = jobstore.ScopedMemoryGlobal
	} else if scopeKey == "" {
		scopeKey = cand.ProjectKey
	}
	if _, _, err := jobstore.NormalizeScopedMemoryScope(scope, scopeKey); err != nil {
		return MemoryCandidateDecision{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	summary, source := strings.TrimSpace(in.Summary), "job:"+cand.JobID
	draft := tracker.Memory{Key: key, Content: cand.Text, MemoryMeta: tracker.MemoryMeta{Kind: kind, Summary: summary, Source: source}}
	if err := tracker.ValidateMemoryForWrite(draft); err != nil {
		return MemoryCandidateDecision{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if old, err := s.meta.GetScopedMemory(scope, scopeKey, key); err == nil && !old.Deleted {
		return MemoryCandidateDecision{}, fmt.Errorf("%w: %s", ErrMemoryCandidateKeyExists, key)
	} else if err != nil && !errors.Is(err, jobstore.ErrScopedMemoryNotFound) {
		return MemoryCandidateDecision{}, err
	}
	claimed, err := s.meta.DecideMemoryCandidate(id, jobstore.MemoryCandidatePending, jobstore.MemoryCandidateAccepted, key, by)
	if err != nil {
		return MemoryCandidateDecision{}, err
	}
	meta := jobstore.ScopedMemoryMetaPatch{Kind: &kind, Source: &source}
	if summary != "" {
		meta.Summary = &summary
	}
	mem, err := s.meta.PutScopedMemoryPatch(scope, scopeKey, key, cand.Text, nil, meta, by)
	if err != nil {
		if _, rerr := s.meta.DecideMemoryCandidate(id, jobstore.MemoryCandidateAccepted, jobstore.MemoryCandidatePending, "", ""); rerr != nil {
			slog.Warn("knowledge capture: release candidate after a failed write", "candidate_id", id, "err", rerr)
		}
		return MemoryCandidateDecision{}, fmt.Errorf("write memory %q: %w", key, err)
	}
	return MemoryCandidateDecision{Candidate: claimed, Memory: &mem}, nil
}

// RejectMemoryCandidate marks a pending candidate rejected (nothing is written anywhere
// else).
func (s *Service) RejectMemoryCandidate(id int64, by string) (jobstore.MemoryCandidate, error) {
	return s.meta.DecideMemoryCandidate(id, jobstore.MemoryCandidatePending, jobstore.MemoryCandidateRejected, "", by)
}
