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
