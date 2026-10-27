package job

import (
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/jobstore"
)

var (
	ErrFollowUpNotLive     = errors.New("job is not running")
	ErrFollowUpEmpty       = errors.New("follow-up prompt is empty")
	ErrFollowUpUnsupported = errors.New("job does not support session continuation")
)

// FollowUp is the public queue projection used by HTTP, CLI and MCP callers.
type FollowUp struct {
	ID             int64  `json:"id"`
	JobID          string `json:"job_id"`
	Prompt         string `json:"prompt"`
	CreatedAt      int64  `json:"created_at"`
	CreatedBy      string `json:"created_by,omitempty"`
	ReviewEach     bool   `json:"review_each,omitempty"`
	Status         string `json:"status"`
	ContinuationID string `json:"continuation_id,omitempty"`
}

func followUpView(r jobstore.FollowUpRecord) FollowUp {
	return FollowUp{ID: r.ID, JobID: r.JobID, Prompt: r.Prompt, CreatedAt: r.CreatedAt, CreatedBy: r.CreatedBy, ReviewEach: r.ReviewEach, Status: r.Status, ContinuationID: r.ContinuationID}
}

// QueueFollowUp adds a request while the source job is live. The source session
// is checked at submission time so an unsupported agent is rejected immediately,
// while the actual continuation waits until the source process has released it.
func (s *Service) QueueFollowUp(jobID, prompt, callerID string, reviewEach bool) (FollowUp, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return FollowUp{}, ErrFollowUpEmpty
	}
	snap, ok := s.Get(jobID)
	if !ok {
		return FollowUp{}, fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	if IsTerminal(snap.Status) || snap.Status == StatusNeedsReview {
		return FollowUp{}, fmt.Errorf("%w: %q is %s", ErrFollowUpNotLive, jobID, snap.Status)
	}
	if snap.SessionID == "" {
		return FollowUp{}, fmt.Errorf("%w: %q has no captured session_id yet", ErrNoSession, jobID)
	}
	resumeAgent := snap.Agent
	if snap.Agent == agent.ExecAgentKey && snap.ResumedFrom != "" {
		if base := s.fallbackBase(snap); !isExecCarrier(base) {
			resumeAgent = base.Agent
		}
	}
	ac, found := s.agents.Get(resumeAgent)
	if !found || !resumable(ac) {
		return FollowUp{}, fmt.Errorf("%w: agent %q", ErrFollowUpUnsupported, resumeAgent)
	}
	r, err := s.meta.AddFollowUp(jobID, prompt, callerID, reviewEach)
	if err != nil {
		return FollowUp{}, err
	}
	return followUpView(r), nil
}

func (s *Service) ListFollowUps(jobID string) ([]FollowUp, error) {
	if _, ok := s.Get(jobID); !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	rows, err := s.meta.ListFollowUps(jobID)
	if err != nil {
		return nil, err
	}
	out := make([]FollowUp, 0, len(rows))
	for _, r := range rows {
		out = append(out, followUpView(r))
	}
	return out, nil
}

func (s *Service) DropFollowUp(jobID string, id int64) error {
	if _, ok := s.Get(jobID); !ok {
		return fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	ok, err := s.meta.DropFollowUp(jobID, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("follow-up %d is not pending", id)
	}
	return nil
}

// processFollowUps starts one continuation for the pending queue after the
// source has genuinely completed. Default entries are merged into one prompt;
// review_each entries are claimed one at a time so a human decision gates the next.
func (s *Service) processFollowUps(snap JobResult) {
	if snap.Status != StatusDone || s.meta == nil {
		return
	}
	items, err := s.meta.ClaimFollowUps(snap.ID, s.nowFn().Unix())
	if err != nil || len(items) == 0 {
		return
	}
	ids := make([]int64, 0, len(items))
	var b strings.Builder
	for i, item := range items {
		ids = append(ids, item.ID)
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%d. %s", i+1, item.Prompt)
	}
	prompt := "The previous task has completed. Continue in the same session and working directory. Complete these follow-up requests in order:\n" + b.String()
	reviewEach := len(items) == 1 && items[0].ReviewEach
	res, err := s.resumeJob(snap.ID, prompt, snap.Runner, snap.CallerID, 0, nil, ResumeOptions{Internal: true, ReviewEach: reviewEach})
	if err != nil {
		_ = s.meta.ReleaseFollowUps(ids)
		return
	}
	_ = s.meta.CompleteFollowUps(ids, res.ID)
	_ = s.meta.MovePendingFollowUps(snap.ID, res.ID)
}
