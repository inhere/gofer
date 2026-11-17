package job

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/inhere/gofer/internal/runner"
)

// staleInteractionReason is the reason recorded on an interaction whose question
// died with the process that asked it.
const staleInteractionReason = "session_restarted"

// expireStaleInteractions closes the interactions a recovered session still shows as
// pending. The agent process that raised them is gone, so no answer can ever reach it;
// the session is reloaded fresh and the user has to ask again. It reuses the cancelled
// status (no new state), records the same event an answer/cancel records with the
// reason, and leaves one line in the session transcript so the timeline can say so.
func (s *Service) expireStaleInteractions(jobID, resultDir string) {
	recs, err := s.meta.ListInteractions(jobID)
	if err != nil {
		slog.Warn("list stale interactions", "job_id", jobID, "err", err)
		return
	}
	now := s.nowFn().Unix()
	expired := 0
	for _, rec := range recs {
		if rec.Status != InteractionPending {
			continue
		}
		rec.Status = InteractionCancelled
		rec.AnsweredAt = now
		if err := s.meta.UpsertInteraction(rec); err != nil {
			slog.Warn("expire stale interaction", "job_id", jobID, "interaction_id", rec.ID, "err", err)
			continue
		}
		expired++
		s.recordEvent(jobID, EventInteractionAnswered, map[string]any{
			"interaction_id": rec.ID,
			"status":         InteractionCancelled,
			"reason":         staleInteractionReason,
		})
	}
	if expired > 0 {
		appendSessionNote(resultDir, staleInteractionReason)
	}
}

// appendSessionNote appends a {t:"session_note"} line to the session's structured
// transcript; the workspace renders it as one line in the conversation. Best-effort:
// a missed note never blocks recovery.
func appendSessionNote(resultDir, code string) {
	if resultDir == "" {
		return
	}
	line, err := json.Marshal(map[string]any{"t": "session_note", "code": code})
	if err != nil {
		return
	}
	path := runner.ACPArtifactPath(resultDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		slog.Debug("append session note", "err", err)
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}
