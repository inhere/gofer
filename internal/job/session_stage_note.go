package job

import (
	"github.com/inhere/gofer/internal/i18n"
)

// StageNoter is the job→stage seam: the per-turn reminder line of the stage flow a session is
// on, looked up by the ids a resident session is known by ("" = it is on no flow). The stage
// service implements it (internal/stage) and the HTTP server injects it, so job never imports
// stage; a process that never injects one (a worker's own service) adds no note.
type StageNoter interface {
	TurnNote(lang i18n.Lang, sids ...string) string
}

// SetStageNotes injects the stage-flow note source (the StageNoter seam).
func (s *Service) SetStageNotes(n StageNoter) { s.stageNotes = n }

// stageNote is the stage line to put in front of the next turn of the resident session the
// jobs / ACP session ids belong to, followed by a blank line; "" when there is none. A
// session is on a flow under the id gofer knows it by: the job id, or the agent's own session
// id (what a resumed or self-registered session says it is).
func (s *Service) stageNote(projectKey string, ids ...string) string {
	n := s.stageNotes
	if n == nil {
		return ""
	}
	lang := i18n.Default
	if cfg := s.cfg.Load(); cfg != nil {
		lang = cfg.LanguageFor(projectKey)
	}
	if line := n.TurnNote(lang, ids...); line != "" {
		return line + "\n\n"
	}
	return ""
}

// stageNoteOf is stageNote for a live session entry.
func (s *Service) stageNoteOf(entry *jobEntry) string {
	if s.stageNotes == nil {
		return ""
	}
	entry.mu.Lock()
	key, id, acpID := entry.result.ProjectKey, entry.result.ID, entry.result.SessionID
	entry.mu.Unlock()
	return s.stageNote(key, id, acpID)
}
