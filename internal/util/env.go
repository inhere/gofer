// Environment layering.
//
// This is the single implementation of the env merging gofer applies before it
// spawns a child process (agent CLI, exec argv, pty session) and of the env-map
// merging the job pipeline performs on the way there. It replaces three
// near-identical mergedEnv copies (acp, runner/local, runner/pty) and two
// map-merge helpers in package job, which had drifted apart (pty returned nil for
// an empty extra, local/acp returned os.Environ) and each spelled out its own
// capacity hint inline (the shape CodeQL reports as go/allocation-size-overflow).

package util

import (
	"os"
	"strings"
)

// Environ returns os.Environ() with extra layered on top, ready for exec.Cmd.Env
// or a pty spec. extra wins on key collision: os/exec (and therefore both the
// local and the pty backend) keeps the LAST occurrence of a duplicated key, so
// appending extra after the inherited environment overrides it. An empty extra
// returns the inherited environment unchanged.
func Environ(extra map[string]string) []string {
	base := os.Environ()
	if len(extra) == 0 {
		return base
	}
	out := make([]string, 0, CapSum(len(base), len(extra)))
	out = append(out, base...)
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

// EnvironWithout is Environ with a DENY list applied to the inherited
// environment (SEC-01): every os.Environ() entry whose key is named in deny is
// dropped before extra is layered on, unless the same key is named in allow.
//
// The filter exists because gofer's own credentials live in the process
// environment — `server.token_env: GOFER_TOKEN` for a serve started from the
// deployment's .env, GOFER_SERVER_TOKEN / GOFER_WORKER_TOKEN for the CLI and the
// worker — and a job process that inherits them is handed the operator's bearer
// token. That is not theoretical: a v0.57 leader job without gofer MCP fell back
// to the inherited server token and reviewed work as the human (design §背景).
//
// Only the INHERITED environment is filtered. `extra` is the caller's own,
// written-down decision (agent/role/job config, gofer metadata) and passes
// through verbatim: those keys are configured deliberately, not inherited by
// accident.
//
// Key matching is case-insensitive, because Windows environment variable names
// are (and os.Environ preserves whatever case it was set with).
func EnvironWithout(deny, allow []string, extra map[string]string) []string {
	base := os.Environ()
	if len(deny) == 0 {
		return Environ(extra)
	}
	out := make([]string, 0, CapSum(len(base), len(extra)))
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		if EnvironKeyDenied(key, deny, allow) {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

// EnvironKeyDenied reports whether key is denied by the deny list and not re-admitted
// by the allow list. Exported because the job pipeline needs the same decision to
// report WHICH allowances actually took effect (job.env_allowed) — a second spelling
// of the rule would let the event disagree with the environment.
func EnvironKeyDenied(key string, deny, allow []string) bool {
	denied := false
	for _, d := range deny {
		if strings.EqualFold(key, d) {
			denied = true
			break
		}
	}
	if !denied {
		return false
	}
	for _, a := range allow {
		if strings.EqualFold(key, a) {
			return false
		}
	}
	return true
}

// EnvironKeysPresent returns the subset of names that the CURRENT process
// environment actually carries a value for, preserving the input order. It is the
// "which of these keys does the allow list really hand to a child" question the
// SEC-01 job.env_allowed event answers, without copying values around.
func EnvironKeysPresent(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	var out []string
	for _, name := range names {
		if _, ok := os.LookupEnv(name); ok {
			out = append(out, name)
		}
	}
	return out
}

// MergeEnv returns base with extra layered on top (extra wins on key collision)
// as a fresh map; neither input is modified. An empty extra returns base itself —
// there is nothing to add, so no copy is needed.
func MergeEnv(base, extra map[string]string) map[string]string {
	if len(extra) == 0 {
		return base
	}
	return EnvWith(base, extra)
}

// EnvWith returns a fresh copy of base with overrides layered on top. Unlike
// MergeEnv it always copies, and overrides always win: it is the seam for
// gofer-owned keys (job metadata, worktree identity, ...) that a caller-supplied
// env key must never shadow, and the result is safe for the caller to keep
// mutating.
func EnvWith(base, overrides map[string]string) map[string]string {
	out := make(map[string]string, CapSum(len(base), len(overrides)))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}
