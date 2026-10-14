package commands

import (
	"os"

	"github.com/inhere/gofer/internal/tracker"
)

// prime_keywords.go resolves the task keywords of a submission (gofer-11tz): the
// subsystems / languages / modules a task touches, which the injected tracker-prime
// matches together with the task's own words to decide which memories it injects in
// full and ranks first.
//
// Two surfaces take them, with the same rule: `job run --prime-keywords` (the job the
// session dispatches) and `repo prime --keywords` (the session's own opening context,
// run by the SessionStart hook, which inherits the shell).
const primeKeywordsEnv = "GOFER_PRIME_KEYWORDS"

// primeKeywordsFromFlag resolves a keyword flag: the EXPLICIT value wins, and only an
// empty flag falls back to GOFER_PRIME_KEYWORDS (comma separated). Explicit first on
// purpose — the environment variable stays in the shell of whoever exported it, so on
// its own it would silently narrow the prime of every later job from that terminal.
func primeKeywordsFromFlag(flagValue string) []string {
	if kw := tracker.SplitPrimeKeywords(flagValue); len(kw) > 0 {
		return kw
	}
	return tracker.SplitPrimeKeywords(os.Getenv(primeKeywordsEnv))
}
