package config

// RetryPolicy bounds how often a job (or a workflow step) is re-run after a
// failure, and how long gofer waits before each re-run (R2/AUTO-03, design §二.2;
// the struct itself is the E24/D16 one, moved here so the config file, the wire
// (JobRequest.Retry) and the four config layers decode from ONE definition —
// internal/job aliases it as job.RetryPolicy).
//
// MaxAttempts counts the FIRST run as attempt 1, so MaxAttempts==3 means up to 2
// retries, and MaxAttempts<=1 means "no retry" (an explicit `max_attempts: 1` is
// how a nearer layer switches retry OFF for good). BackoffSec is the退避表 indexed
// by the just-failed attempt; empty falls back to the built-in table
// [30,120,300,900,3600]. OnExitCodes, when non-empty, restricts the retry to those
// exit codes (empty == retry on any non-zero exit).
//
// The SEMANTICS live in internal/job (MaxAttemptsPolicy / BackoffForPolicy /
// RetryableExitPolicy), which the job-level and step-level retry paths share.
type RetryPolicy struct {
	MaxAttempts int   `json:"max_attempts" yaml:"max_attempts"`                       // >=1 (includes the first run)
	BackoffSec  []int `json:"backoff_sec,omitempty" yaml:"backoff_sec,omitempty"`     // 默认接 SR606 [30,120,300,900,3600]
	OnExitCodes []int `json:"on_exit_codes,omitempty" yaml:"on_exit_codes,omitempty"` // 空=任意非0退出重试
}

// EffectiveRetryPolicy resolves the retry policy that applies to a job, nearest
// layer first:
//
//	requested (JobRequest.Retry)      → used as given (an explicit max_attempts:1 = off)
//	projects.<projectKey>.retry       → that project's policy
//	agents.<agentKey>.retry           → that agent's policy
//	server.retry                      → the deployment default
//	otherwise                         → nil = retry OFF (the default; retry never
//	                                     turns itself on for a config that never
//	                                     mentioned it)
//
// The NEAREST layer that says anything wins WHOLESALE: a layer never merges field
// by field with the one below it, so "half a policy" (one layer's max_attempts with
// another's backoff table or exit-code filter) cannot be expressed or accidentally
// inherited. That is also why `max_attempts: 1` at a nearer layer is final — it is
// the layer's whole answer, not a field an outer layer may top up. A nil pointer at
// a layer means "this layer says nothing".
//
// agentKey is the RESOLVED agent of the job (a role/template-filled agent resolves
// on the agent that actually runs), mirroring EffectiveStallTimeoutSec.
func (c *Config) EffectiveRetryPolicy(projectKey, agentKey string, requested *RetryPolicy) *RetryPolicy {
	if requested != nil {
		return requested
	}
	if c == nil {
		return nil
	}
	if proj, ok := c.Projects[projectKey]; ok && proj.Retry != nil {
		return proj.Retry
	}
	if a, ok := c.Agents[agentKey]; ok && a.Retry != nil {
		return a.Retry
	}
	if c.Server.Retry != nil {
		return c.Server.Retry
	}
	return nil
}
