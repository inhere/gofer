package job

// RetryPolicy bounds per-step (and per-job, E24) retry on failure (P1, design
// §5.1, D16). MaxAttempts counts the FIRST run as attempt 1, so MaxAttempts==3
// means up to 2 retries. BackoffSec is the退避表 indexed by the just-failed
// attempt (defaults to the SR606 table when empty). OnExitCodes, when non-empty,
// restricts retry to those exit codes (empty == retry on any non-zero exit /
// timeout / failure, see RetryableExitPolicy).
//
// It is a job-model type (JobRequest.Retry) shared by single-job retry (execute.go)
// and workflow step retry (internal/job/workflow), so it stays in package job and
// the workflow sub-package references it as job.RetryPolicy (layering design §13.4).
type RetryPolicy struct {
	MaxAttempts int   `json:"max_attempts" yaml:"max_attempts"`                       // >=1 (includes the first run)
	BackoffSec  []int `json:"backoff_sec,omitempty" yaml:"backoff_sec,omitempty"`     // 默认接 SR606 [30,120,300,900,3600]
	OnExitCodes []int `json:"on_exit_codes,omitempty" yaml:"on_exit_codes,omitempty"` // 空=任意非0退出重试
}

// maxRetryAttempts caps RetryPolicy.MaxAttempts so a misconfigured workflow can
// not retry forever (defence against失控 retry storms).
const maxRetryAttempts = 10

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
