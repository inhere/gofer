package sources

import (
	"context"
	"path/filepath"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/memorysynthesis"
	"github.com/inhere/gofer/internal/store"
)

// JobReader is the part of *jobstore.Store the job adapter reads.
type JobReader interface {
	ListJobs(jobstore.ListQuery) ([]jobstore.JobRecord, error)
}

// ReportTailFunc returns the stdout tail (the job's report) of a finished job, at most
// maxBytes. A job without output yields "".
type ReportTailFunc func(rec jobstore.JobRecord, maxBytes int64) string

// FileReportTail reads the tail from the job's result directory, the same place job
// comments and the delivery check read it. A missing log is simply no report.
func FileReportTail(rec jobstore.JobRecord, maxBytes int64) string {
	if rec.ResultDir == "" {
		return ""
	}
	b, err := store.NewFileStore(filepath.Dir(rec.ResultDir)).ReadLogTail(rec.ID, store.StreamStdout, maxBytes)
	if err != nil {
		return ""
	}
	return string(b)
}

// Jobs collects jobs that reached a terminal state inside the window, with a bounded
// report tail. Internal jobs (steward, summarizer, synthesis itself) are excluded.
type Jobs struct {
	Config
	Store      JobReader
	ReportTail ReportTailFunc
	// MaxJobs, TailBytes and TailLines bound the number of jobs and each report excerpt.
	MaxJobs   int
	TailBytes int64
	TailLines int
}

var terminalJobStatus = map[string]bool{"done": true, "failed": true, "cancelled": true, "timeout": true, "rejected": true}

func (s Jobs) Collect(_ context.Context, project, day string) ([]memorysynthesis.Item, error) {
	w, err := s.window(day)
	if err != nil {
		return nil, err
	}
	maxJobs, tailBytes, lines := s.MaxJobs, s.TailBytes, s.TailLines
	if maxJobs <= 0 {
		maxJobs = 500
	}
	if tailBytes <= 0 {
		tailBytes = 4096
	}
	if lines <= 0 {
		lines = 30
	}
	// A job that ended today may have started yesterday: look one day further back.
	recs, err := s.Store.ListJobs(jobstore.ListQuery{Project: project, ExcludeTags: jobstore.InternalJobTags,
		Since: w.Start.Unix() - 24*3600, Limit: maxJobs})
	if err != nil {
		return nil, err
	}
	var out []memorysynthesis.Item
	for _, r := range recs {
		if r.ProjectKey != project || !terminalJobStatus[r.Status] || !w.Contains(r.EndedAt) {
			continue
		}
		var tail string
		if s.ReportTail != nil {
			tail = tailLines(s.ReportTail(r, tailBytes), lines)
		}
		out = append(out, memorysynthesis.Item{
			Kind: KindJobReport, OccurredAt: stamp(w, r.EndedAt), Title: "job " + r.ID + " " + r.Status,
			SourceRef: "job:" + r.ID + "#report",
			Text:      kv("status", r.Status, "error", r.Error, "report", tail),
		})
	}
	return out, nil
}
