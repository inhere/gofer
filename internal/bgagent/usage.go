package bgagent

import (
	"encoding/json"
	"math"

	"github.com/inhere/gofer/internal/runner"
)

// UsageSnapshot is the one usage figure a run leaves behind, normalised across providers:
// the billing counters of the run and the context level the rotation reads. The parsing
// of each provider's NDJSON lives in runner / runner/ndjsonfilter (one reader per
// dialect, shared with the job layer); this is the projection the runner stores.
type UsageSnapshot struct {
	Usage *runner.Usage
	// ContextUsed / ContextLimit are 0 when unknown (never a made-up window).
	ContextUsed  int64
	ContextLimit int64
	// Ratio is the context fill; RatioKnown is false when the agent reported no level.
	Ratio      float64
	RatioKnown bool
	// Source names where the context level came from (runner.ContextSource*).
	Source string
}

// ratioScale is the window a level reported only as a ratio is stored against, so the
// session table's used / limit pair still reproduces the ratio.
const ratioScale = 1_000_000

// SnapshotFromUsage projects a run's usage (nil = the agent reported none).
func SnapshotFromUsage(u *runner.Usage) UsageSnapshot {
	s := UsageSnapshot{Usage: u}
	if u == nil || u.Context == nil {
		return s
	}
	c := u.Context
	ratio, ok := c.Fraction()
	if !ok {
		// A window with no fill (or the reverse) is not a level: report none.
		s.ContextUsed, s.ContextLimit, s.Source = c.UsedTokens, c.LimitTokens, c.Source
		return s
	}
	s.Ratio, s.RatioKnown, s.Source = ratio, true, c.Source
	s.ContextUsed, s.ContextLimit = c.UsedTokens, c.LimitTokens
	if s.ContextLimit <= 0 {
		// Only the agent's own ratio is known: store it against a fixed scale so the
		// used / limit pair of the session table still reproduces it.
		s.ContextLimit = ratioScale
		s.ContextUsed = int64(math.Round(ratio * ratioScale))
	}
	return s
}

// usageJSON is the compact JSON stored with a run / session ("" when there is no usage).
func usageJSON(u *runner.Usage) string {
	if u == nil {
		return ""
	}
	b, err := json.Marshal(u)
	if err != nil {
		return ""
	}
	return string(b)
}
