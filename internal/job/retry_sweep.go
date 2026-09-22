package job

import (
	"encoding/json"
	"log/slog"
	"slices"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/util"
)

// retryTag is stamped on every re-submitted job, and retryOfTagPrefix names the job
// it re-runs (`retry_of:<source job id>`) — `job list --tag retry` then finds every
// re-run, and one tag pins which failure each of them belongs to.
const (
	retryTag         = "retry"
	retryOfTagPrefix = "retry_of:"
)

// SweepDueRetries runs ONE pass of the durable retry queue (R2/AUTO-03): claim up to
// limit due rows with a lease, and submit each as its own new job. internal/serve's
// retry loop calls it every tick; tests drive the same function directly.
//
// Per claimed row:
//   - submit the stored request as attempt rec.Attempt (no idempotency key, always
//     async), tagged `retry` / `retry_of:<source job>` so the re-run is findable;
//   - on success mark the row done (recording new_job_id) and record
//     job.retry_started on the SOURCE job's timeline;
//   - on failure RELEASE the row with the next backoff step, so the retry is late by
//     one step instead of lost, and count it as failed.
//
// A row whose request_json cannot be decoded can never be submitted: it is cancelled
// (with a warning) rather than re-claimed on every tick forever.
//
// The fields request_json cannot carry are restored here: JobRequest.Attempt comes
// from the row's own column (it is json:"-"), and CallerID / SourceJobID from the
// source job's row — exactly the values the in-process path restored from the
// snapshot before submission.
//
// started/failed count the rows submitted and the rows that could not be, so the
// caller can log a tick without parsing anything; a store-level failure (the claim
// query itself) is returned as err and nothing is submitted.
func (s *Service) SweepDueRetries(now int64, limit int, lease int64) (started, failed int, err error) {
	due, err := s.meta.ClaimDueRetries(now, limit, lease)
	if err != nil {
		return 0, 0, err
	}
	for _, rec := range due {
		var req JobRequest
		if uerr := json.Unmarshal([]byte(rec.RequestJSON), &req); uerr != nil {
			slog.Warn("retry sweep: decode request", "retry_id", rec.ID, "err", uerr)
			if cerr := s.meta.CancelRetry(rec.ID); cerr != nil {
				slog.Warn("retry sweep: cancel undecodable retry", "retry_id", rec.ID, "err", cerr)
			}
			failed++
			continue
		}
		req.Attempt = rec.Attempt
		req.RequestID = ""
		req.Sync = false
		req.Tags = retryTagList(req.Tags, rec.SourceJobID)
		// json:"-" lineage: the source job's row is the only place that still has it
		// (it is not in request_json). A pruned source leaves it empty, which is what
		// a retry of an unknown origin can honestly claim.
		if src, ok := s.Get(rec.SourceJobID); ok {
			req.CallerID = src.CallerID
			req.SourceJobID = src.SourceJobID
		}
		res, serr := s.Submit(req)
		if serr != nil {
			// Not lost: the row goes back to pending one backoff step later. The
			// policy is re-resolved here because a config-level policy is not in
			// request_json either; BackoffForPolicy(nil, n) is the built-in table.
			slog.Warn("retry sweep: submit", "retry_id", rec.ID,
				"job_id", rec.SourceJobID, "attempt", rec.Attempt, "err", serr)
			policy := s.config().EffectiveRetryPolicy(req.ProjectKey, req.Agent, req.Retry)
			next := now + int64(BackoffForPolicy(policy, rec.Attempt))
			if rerr := s.meta.ReleaseRetry(rec.ID, next); rerr != nil {
				slog.Warn("retry sweep: release", "retry_id", rec.ID, "err", rerr)
			}
			failed++
			continue
		}
		if merr := s.meta.MarkRetryDone(rec.ID, res.ID); merr != nil {
			slog.Warn("retry sweep: mark done", "retry_id", rec.ID, "new_job_id", res.ID, "err", merr)
		}
		s.recordEvent(rec.SourceJobID, EventJobRetryStarted,
			map[string]any{"retry_id": rec.ID, "new_job_id": res.ID})
		started++
	}
	return started, failed, nil
}

// retryTagList returns a re-submitted job's tags: the source job's own tags plus
// `retry` and `retry_of:<source job>`, without duplicating a tag the request already
// carried (a hand-written row may name them). The input slice is never mutated — a
// JobRequest is shared with its snapshot.
func retryTagList(tags []string, sourceJobID string) []string {
	extra := []string{retryTag}
	if sourceJobID != "" {
		extra = append(extra, retryOfTagPrefix+sourceJobID)
	}
	out := make([]string, 0, util.CapSum(len(tags), len(extra)))
	out = append(out, tags...)
	for _, t := range extra {
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// ListRetries returns a job's durable retry chain (oldest attempt first). Reads are
// not gated: a retry is job metadata, exactly like the job's events or wakeups.
func (s *Service) ListRetries(jobID string) ([]jobstore.RetryRecord, error) {
	return s.meta.ListRetriesByJob(jobID)
}

// CancelRetry drops a PENDING (or currently claimed) retry, so the sweeper will not
// submit it — the `job retry cancel <retry-id>` path. A row that already ran or was
// already cancelled is terminal and reports an error rather than pretending to have
// cancelled something.
func (s *Service) CancelRetry(id string) error {
	return s.meta.CancelRetry(id)
}

// RetryMaxAttempts returns the attempt ceiling a retry row was scheduled under — the
// M in the `attempt N/M` a human reads — by decoding the policy maybeRetryJob stamped
// on the row's request. 0 means the row carries no policy (a hand-written row, or one
// written before the stamp existed), and callers render the attempt without a ceiling
// rather than inventing one.
func RetryMaxAttempts(rec jobstore.RetryRecord) int {
	var req struct {
		Retry *RetryPolicy `json:"retry"`
	}
	if rec.RequestJSON == "" || json.Unmarshal([]byte(rec.RequestJSON), &req) != nil {
		return 0
	}
	if req.Retry == nil || req.Retry.MaxAttempts < 1 {
		return 0
	}
	return req.Retry.MaxAttempts
}
