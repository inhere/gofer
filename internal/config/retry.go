package config

import (
	"encoding/json"
	"fmt"

	yaml "github.com/goccy/go-yaml"
)

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
	MaxAttempts int        `json:"max_attempts" yaml:"max_attempts"`                       // >=1 (includes the first run)
	BackoffSec  BackoffSec `json:"backoff_sec,omitempty" yaml:"backoff_sec,omitempty"`     // 默认接 SR606 [30,120,300,900,3600]
	OnExitCodes []int      `json:"on_exit_codes,omitempty" yaml:"on_exit_codes,omitempty"` // 空=任意非0退出重试
}

// BackoffSec is the retry backoff table, in seconds. It decodes from BOTH spellings a
// person writes in YAML or JSON: the list (`backoff_sec: [300, 600]`) and a single
// number (`backoff_sec: 300`), the latter meaning the SAME wait before every retry. A
// single number that had to be written as a one-element list was the most common way to
// get "int was used where sequence is expected" from a workflow file (gofer-pn0x).
//
// Encoding always emits the list form, so a round-tripped config states the meaning
// explicitly and never depends on which spelling a reader understands.
type BackoffSec []int

// UnmarshalYAML accepts either form (goccy's raw-bytes unmarshaler hook).
func (b *BackoffSec) UnmarshalYAML(data []byte) error {
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return err
	}
	return b.set(v)
}

// UnmarshalJSON accepts either form: a number or an array of numbers.
func (b *BackoffSec) UnmarshalJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	return b.set(v)
}

// set normalizes a decoded scalar or sequence into the table.
func (b *BackoffSec) set(v any) error {
	switch t := v.(type) {
	case nil:
		*b = nil
	case []any:
		table := make([]int, 0, len(t))
		for _, e := range t {
			n, err := backoffSeconds(e)
			if err != nil {
				return err
			}
			table = append(table, n)
		}
		*b = table
	default:
		n, err := backoffSeconds(t)
		if err != nil {
			return err
		}
		*b = BackoffSec{n}
	}
	return nil
}

// backoffSeconds reads one decoded value as a whole number of seconds. YAML yields int /
// uint64 and JSON yields float64 for the same literal, so all three are accepted; a
// fractional or non-numeric value is rejected with the spelling that works.
func backoffSeconds(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case uint64:
		return int(n), nil
	case float64:
		if n != float64(int(n)) {
			return 0, fmt.Errorf("retry.backoff_sec: %v is not a whole number of seconds", n)
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("retry.backoff_sec: want a number of seconds or a list of them, got %v", v)
	}
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
