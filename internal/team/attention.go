package team

import "github.com/inhere/gofer/internal/jobstore"

// AttentionFact is a team that needs a person: its leader missed MissingEvalLimit evaluations in a
// row. It is derived from the stored evaluations on every read, so it goes away by itself when
// the leader records a real one (or the team is archived).
type AttentionFact struct {
	TeamID     string
	Name       string
	ProjectKey string
	Kind       string
	// Since is when the newest missing_eval was written.
	Since int64
}

// Fact kinds.
const AttentionMissingEval = "missing_eval"

// Attention lists the active teams whose newest MissingEvalLimit evaluations are all
// missing_eval. allow narrows by project (nil = every project).
func (s *Service) Attention(allow func(project string) bool) ([]AttentionFact, error) {
	teams, err := s.store.ListTeams(jobstore.TeamFilter{})
	if err != nil {
		return nil, err
	}
	var out []AttentionFact
	for _, t := range teams {
		if allow != nil && !allow(t.ProjectKey) {
			continue
		}
		evs, err := s.store.ListTeamEvals(t.ID, MissingEvalLimit)
		if err != nil {
			return nil, err
		}
		if len(evs) < MissingEvalLimit {
			continue
		}
		all := true
		for _, e := range evs {
			if e.Outcome != jobstore.TeamEvalMissing {
				all = false
				break
			}
		}
		if all {
			out = append(out, AttentionFact{TeamID: t.ID, Name: t.Name, ProjectKey: t.ProjectKey, Kind: AttentionMissingEval, Since: evs[0].At})
		}
	}
	return out, nil
}
