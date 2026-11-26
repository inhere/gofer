package stage

import (
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/stagenote"
)

// BriefOf summarises a flow for a session view. It never evaluates a gate (that may run git),
// so it is safe on every heartbeat.
func BriefOf(v View) stagenote.Brief {
	b := stagenote.Brief{
		WorkItemID: v.WorkItemID, Step: v.Step, StepNo: v.StepIndex + 1, StepTotal: len(v.Steps), Status: v.Status,
	}
	if v.Status == "blocked" {
		b.Reason = v.BlockedReason
	}
	return b.Fit()
}

// BriefForSession returns the `stage` object of a session's view: the flow of the work item
// the session is on, or — with none in progress — an Enabled-only object when the project
// has the flow on (projectKey "" has no project to opt out of, so it gets nothing). nil when
// there is nothing to say or the lookup failed (a view never fails over its stage).
func (s *Service) BriefForSession(sid, projectKey string) *stagenote.Brief {
	v, ok, err := s.ForSession(sid, false)
	if err != nil {
		return nil
	}
	if ok {
		b := BriefOf(v)
		return &b
	}
	if projectKey != "" && s.Enabled(projectKey) {
		return &stagenote.Brief{Enabled: true}
	}
	return nil
}

// TurnNote is the per-turn reminder line of a session that has a flow in progress (ACP
// sessions take it in front of every turn); "" when it has none. It reads the stored flow
// only, never a gate.
func (s *Service) TurnNote(sid string, lang i18n.Lang) string {
	v, ok, err := s.ForSession(sid, false)
	if err != nil || !ok {
		return ""
	}
	return stagenote.Line(lang, BriefOf(v))
}
