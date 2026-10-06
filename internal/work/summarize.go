package work

import (
	"github.com/inhere/gofer/internal/jobstore"
)

// Why a tidy-up runs.
const (
	CauseManual  = "manual"  // the 「整理」 button / `gofer work summarize`
	CauseRequest = "request" // a report / hand-over request that could not be answered
	CauseAuto    = "auto"    // the passive scan (idle / offline / ended with new activity)
)

// SummarizeOpts describes one tidy-up run.
type SummarizeOpts struct {
	Cause     string
	RequestID string
	SessionID string
	By        string
}

// StartSummarize runs a tidy-up in the background (placeholder until the summarizer
// lands: the request is failed with an honest reason).
func (s *Service) StartSummarize(itemID string, o SummarizeOpts) {
	if o.RequestID != "" {
		_, _, _ = s.store.MarkWorkRequest(o.RequestID, jobstore.WorkRequestFailed, "", "整理器未启用")
	}
}

func (s *Service) summarizing(requestID string) bool { return false }
