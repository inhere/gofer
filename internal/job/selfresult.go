package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
)

// Self-reported terminal state (gofer_submit_result): the job's own agent says how it
// judges its run. It is recorded and shown; it never changes the job's status, never
// resumes it and never fails it.
const (
	ResultPass   = "pass"
	ResultRework = "rework"
	ResultFail   = "fail"
	ResultHuman  = "human"

	// EventJobResultReported is recorded when a job reports its own outcome:
	// {decision,reason}.
	EventJobResultReported = "job.result_reported"

	// Caps keep a report a short statement, not a transcript.
	maxSelfResultReason   = 2000
	maxSelfResultAction   = 1000
	maxSelfResultFindings = 20
	maxSelfResultFinding  = 1000
)

// ErrSelfResultExists is returned when the job already reported its outcome (once per
// job; a continuation or retry is a new job and reports for itself).
var ErrSelfResultExists = errors.New("job already reported a result")

// SelfResultInput is the body of a self-reported terminal state.
type SelfResultInput struct {
	Decision       string   `json:"decision"`
	Reason         string   `json:"reason"`
	ActionRequired string   `json:"action_required,omitempty"`
	Findings       []string `json:"findings,omitempty"`
}

// ValidSelfResultDecision reports whether d is one of the four accepted decisions.
func ValidSelfResultDecision(d string) bool {
	switch d {
	case ResultPass, ResultRework, ResultFail, ResultHuman:
		return true
	}
	return false
}

// SubmitSelfResult records the outcome job id's agent reports about itself and returns
// the job's refreshed snapshot. Invalid input wraps ErrInvalidRequest; a second report
// for the same job returns ErrSelfResultExists. operator is the credential's job id.
func (s *Service) SubmitSelfResult(id string, in SelfResultInput, operator string) (JobResult, error) {
	in.Decision = strings.TrimSpace(in.Decision)
	in.Reason = strings.TrimSpace(in.Reason)
	in.ActionRequired = strings.TrimSpace(in.ActionRequired)
	if !ValidSelfResultDecision(in.Decision) {
		return JobResult{}, fmt.Errorf("%w: decision must be one of pass, rework, fail, human", ErrInvalidRequest)
	}
	if in.Reason == "" {
		return JobResult{}, fmt.Errorf("%w: reason is required", ErrInvalidRequest)
	}
	if len([]rune(in.Reason)) > maxSelfResultReason {
		return JobResult{}, fmt.Errorf("%w: reason must be at most %d characters", ErrInvalidRequest, maxSelfResultReason)
	}
	if len([]rune(in.ActionRequired)) > maxSelfResultAction {
		return JobResult{}, fmt.Errorf("%w: action_required must be at most %d characters", ErrInvalidRequest, maxSelfResultAction)
	}
	if len(in.Findings) > maxSelfResultFindings {
		return JobResult{}, fmt.Errorf("%w: at most %d findings", ErrInvalidRequest, maxSelfResultFindings)
	}
	for i, f := range in.Findings {
		in.Findings[i] = strings.TrimSpace(f)
		if len([]rune(in.Findings[i])) > maxSelfResultFinding {
			return JobResult{}, fmt.Errorf("%w: a finding must be at most %d characters", ErrInvalidRequest, maxSelfResultFinding)
		}
	}
	report, err := json.Marshal(in)
	if err != nil {
		return JobResult{}, fmt.Errorf("encode job result: %w", err)
	}
	attempt, err := s.meta.SetJobSelfResult(id, in.Decision, in.Reason, string(report))
	if err != nil {
		if errors.Is(err, jobstore.ErrSelfResultExists) {
			return JobResult{}, ErrSelfResultExists
		}
		if strings.Contains(err.Error(), "not found") {
			return JobResult{}, fmt.Errorf("unknown job %q", id)
		}
		return JobResult{}, err
	}
	if entry := s.entry(id); entry != nil {
		entry.mu.Lock()
		entry.result.ResultDecision = in.Decision
		entry.result.ResultReason = in.Reason
		entry.result.ResultReportJSON = string(report)
		entry.result.ResultAttempt = attempt
		out := entry.result
		entry.mu.Unlock()
		s.recordEvent(id, EventJobResultReported, map[string]any{"decision": in.Decision, "reason": in.Reason, "operator": operator})
		return out, nil
	}
	rec, ok, err := s.meta.GetJob(id)
	if err != nil {
		return JobResult{}, err
	}
	if !ok {
		return JobResult{}, fmt.Errorf("unknown job %q", id)
	}
	s.recordEvent(id, EventJobResultReported, map[string]any{"decision": in.Decision, "reason": in.Reason, "operator": operator})
	return fromRecord(rec), nil
}

// DecodeSelfResultReport decodes the stored report document into out; false when there is
// none (or it is unreadable).
func DecodeSelfResultReport(reportJSON string, out *SelfResultInput) bool {
	if reportJSON == "" {
		return false
	}
	return json.Unmarshal([]byte(reportJSON), out) == nil
}
