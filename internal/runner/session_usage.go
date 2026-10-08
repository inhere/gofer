package runner

// SessionUsage is the token accounting of one terminal agent-CLI session (N2 §A,
// OBS-14), in the same Usage shape a job uses. The hook reads the agent's transcript
// and reports DELTAS (since its last report); the server adds them up, so the same
// type is both the heartbeat's `usage_delta` and the session's stored total.
//
// Main is the session's own conversation, Sub everything its sub-agents burned
// (sidechain rows plus the transcript's subagents/*.jsonl files). ByModel splits the
// SAME tokens (main + sub) by the model that produced them, so Main+Sub equals the
// sum of ByModel for a transcript that names its models.
type SessionUsage struct {
	Main    Usage            `json:"main"`
	Sub     Usage            `json:"sub"`
	ByModel map[string]Usage `json:"by_model,omitempty"`
}

// Empty reports that nothing was counted (so there is nothing to report or store).
func (s SessionUsage) Empty() bool {
	return s.Main.IsZero() && s.Sub.IsZero() && len(s.ByModel) == 0
}

// Add folds o into s (counters add; ByModel merges per model).
func (s *SessionUsage) Add(o SessionUsage) {
	s.Main = s.Main.Plus(o.Main)
	s.Sub = s.Sub.Plus(o.Sub)
	for m, u := range o.ByModel {
		if s.ByModel == nil {
			s.ByModel = make(map[string]Usage, len(o.ByModel))
		}
		s.ByModel[m] = s.ByModel[m].Plus(u)
	}
}

// Total is Main + Sub.
func (s SessionUsage) Total() Usage { return s.Main.Plus(s.Sub) }

// IsZero reports that no counter and no cost is set (Source is a label, not a count).
func (u Usage) IsZero() bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadTokens == 0 &&
		u.CacheWriteTokens == 0 && u.TotalTokens == 0 && u.CostUSD == 0
}

// Plus returns the counter-wise sum of u and o. Source keeps u's label, falling back
// to o's, so an accumulated tally stays labelled.
func (u Usage) Plus(o Usage) Usage {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheReadTokens += o.CacheReadTokens
	u.CacheWriteTokens += o.CacheWriteTokens
	u.TotalTokens += o.TotalTokens
	u.CostUSD += o.CostUSD
	if u.Source == "" {
		u.Source = o.Source
	}
	return u
}

// UsageSourceTranscriptPrefix + dialect labels usage read from an agent's own
// transcript (terminal sessions, N2 §A), e.g. "transcript:claude".
const UsageSourceTranscriptPrefix = "transcript:"
