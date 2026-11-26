package job

import (
	"github.com/inhere/gofer/internal/i18n"
)

// TeamNoter is the job→team seam: the leader-protocol block a session on a team's leader seat
// takes in front of every turn (plan 2026-11-26-team-plan.md §2.4), the way StageNoter carries the
// stage line. The team protocol engine implements it (internal/team) and the HTTP server injects
// it, so job never imports team; a process that never injects one (a worker's own service) adds
// no note, and a session that sits on no leader seat gets "" (no team ⇒ no change).
//
// TurnNote opens a turn of the leader and returns the block; PeekNote returns the same block
// without opening one (a re-sent say belongs to the turn its first send opened); TurnEnded closes
// the turn when the job goes back to awaiting input. All take the ids a resident session is known
// by (its job id first).
type TeamNoter interface {
	TurnNote(lang i18n.Lang, ids ...string) string
	PeekNote(lang i18n.Lang, ids ...string) string
	TurnEnded(ids ...string)
}

// SetTeamNotes injects the leader-protocol note source (the TeamNoter seam).
func (s *Service) SetTeamNotes(n TeamNoter) { s.teamNotes = n }

func (s *Service) noteLang(projectKey string) i18n.Lang {
	if cfg := s.cfg.Load(); cfg != nil {
		return cfg.LanguageFor(projectKey)
	}
	return i18n.Default
}

// teamNote is the leader block to put in front of the next turn of the resident session the ids
// belong to ("" when it sits on no leader seat). It opens a turn.
func (s *Service) teamNote(projectKey string, ids ...string) string {
	if s.teamNotes == nil {
		return ""
	}
	return s.teamNotes.TurnNote(s.noteLang(projectKey), ids...)
}

// teamNoteOf is teamNote for a live session entry.
func (s *Service) teamNoteOf(entry *jobEntry) string {
	if s.teamNotes == nil {
		return ""
	}
	key, id, acpID := sessionNoteIDs(entry)
	return s.teamNote(key, id, acpID)
}

// teamPeekOf is the block of a say that is sent again: the same text, no new turn.
func (s *Service) teamPeekOf(entry *jobEntry) string {
	if s.teamNotes == nil {
		return ""
	}
	key, id, acpID := sessionNoteIDs(entry)
	return s.teamNotes.PeekNote(s.noteLang(key), id, acpID)
}

// teamTurnEnded tells the leader protocol a resident session went back to awaiting input.
func (s *Service) teamTurnEnded(jobID string) {
	if s.teamNotes != nil {
		s.teamNotes.TurnEnded(jobID)
	}
}

func sessionNoteIDs(entry *jobEntry) (projectKey, id, acpID string) {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.result.ProjectKey, entry.result.ID, entry.result.SessionID
}
