package job

import "github.com/inhere/gofer/internal/config"

// RetryPolicy is the retry policy shared by single-job retry (execute.go +
// retry_sweep.go), workflow step retry (internal/job/workflow) and the config
// layers (R2/AUTO-03: server.retry / agents.<k>.retry / projects.<k>.retry —
// config.EffectiveRetryPolicy). The STRUCT lives in internal/config so the config
// file, the wire (JobRequest.Retry) and the four layers decode from one definition;
// this alias keeps the job-model name every call site already uses.
//
// It is a job-model type (JobRequest.Retry), so the workflow sub-package references
// it as job.RetryPolicy (layering design §13.4). The SEMANTICS — the attempt
// ceiling, the退避表 and the exit-code filter — stay here as the three pure
// functions below, which every retry path shares.
type RetryPolicy = config.RetryPolicy

// defaultBackoffSec is the SR606退避表 used when a RetryPolicy gives no explicit
// BackoffSec: 30s → 2min → 5min → 15min → 60min, the last entry reused past the
// end (mirrors the E14 deliveryBackoff table).
var defaultBackoffSec = []int{30, 120, 300, 900, 3600}

// MaxAttemptsPolicy returns a RetryPolicy's attempt ceiling (MaxAttempts), or 1 (no
// retry) when the policy is nil / unset. Shared by step-level and job-level retry.
func MaxAttemptsPolicy(p *RetryPolicy) int {
	if p == nil || p.MaxAttempts < 1 {
		return 1
	}
	return p.MaxAttempts
}

// BackoffForPolicy returns the backoff (seconds) before re-running an attempt that
// just failed. attempt is the 1-based number of the run that just failed; the
// backoff table is indexed by attempt-1 (attempt 1 → table[0]), clamped to the last
// entry past the end (SR606). An empty/absent BackoffSec falls back to the SR606
// defaultBackoffSec. Shared by step-level and job-level retry (one semantics).
func BackoffForPolicy(p *RetryPolicy, attempt int) int {
	table := defaultBackoffSec
	if p != nil && len(p.BackoffSec) > 0 {
		table = p.BackoffSec
	}
	idx := attempt - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(table) {
		idx = len(table) - 1
	}
	return table[idx]
}

// RetryableExitPolicy reports whether a failure with exitCode is retryable under a
// RetryPolicy.OnExitCodes. An empty/absent OnExitCodes means "retry on any non-zero
// exit" (the default). When OnExitCodes is set, only those exit codes are retried.
// Shared by step-level and job-level retry (one semantics).
func RetryableExitPolicy(p *RetryPolicy, exitCode int) bool {
	if p == nil || len(p.OnExitCodes) == 0 {
		return true
	}
	for _, c := range p.OnExitCodes {
		if c == exitCode {
			return true
		}
	}
	return false
}
