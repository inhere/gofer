package job

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/store"
)

func (s *Service) cancelSessionReplyTimerLocked(entry *jobEntry) {
	if entry.awaitReplyTimer != nil {
		entry.awaitReplyTimer.Stop()
		entry.awaitReplyTimer = nil
	}
	entry.awaitReplyGeneration++
	entry.awaitReplyNotified = false
}

func (s *Service) scheduleSessionReply(entry *jobEntry) {
	cfg := s.config()
	delay := configSessionReplyDelay(cfg)
	entry.mu.Lock()
	if !entry.result.Session || entry.result.Status != StatusAwaitingInput || delay <= 0 {
		s.cancelSessionReplyTimerLocked(entry)
		entry.mu.Unlock()
		return
	}
	s.cancelSessionReplyTimerLocked(entry)
	entry.awaitReplyGeneration++
	generation := entry.awaitReplyGeneration
	entry.awaitReplyNotified = false
	timer := time.AfterFunc(time.Duration(delay)*time.Second, func() {
		s.fireSessionReplyReminder(entry, generation)
	})
	entry.awaitReplyTimer = timer
	entry.mu.Unlock()
}

func configSessionReplyDelay(cfg *config.Config) int {
	if cfg == nil || cfg.Server.Notification == nil {
		return 0
	}
	return cfg.Server.Notification.EffectiveSessionReplyDelaySec()
}

func (s *Service) fireSessionReplyReminder(entry *jobEntry, generation uint64) {
	entry.mu.Lock()
	if generation != entry.awaitReplyGeneration || entry.awaitReplyNotified ||
		!entry.result.Session || entry.result.Status != StatusAwaitingInput {
		entry.mu.Unlock()
		return
	}
	entry.awaitReplyNotified = true
	entry.awaitReplyTimer = nil
	snap := entry.result
	entry.mu.Unlock()

	detail := map[string]any{
		"turn_no":          snap.TurnNo,
		"idle_deadline_at": snap.IdleDeadlineAt,
		"agent":            snap.Agent,
		"project":          snap.ProjectKey,
		"title":            snap.Title,
		"reply_preview":    sessionReplyPreview(snap.ResultDir, snap.TurnNo, 64*1024),
		"session_id":       snap.SessionID,
	}
	s.recordEvent(snap.ID, EventSessionAwaitingReply, detail)
}

func sessionReplyPreview(resultDir string, turn, maxRunes int) string {
	if strings.TrimSpace(resultDir) == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(resultDir, store.StdoutFile))
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(raw))
	if turn > 0 {
		marker := fmt.Sprintf("--- turn %d ---", turn)
		if index := strings.LastIndex(text, marker); index >= 0 {
			text = strings.TrimSpace(text[index+len(marker):])
		}
	}
	if maxRunes > 0 {
		runes := []rune(text)
		if len(runes) > maxRunes {
			runes = runes[:maxRunes]
		}
		return string(runes)
	}
	return text
}

func (s *Service) sessionReplyLink(snap JobResult) string {
	if snap.SessionID != "" {
		thread := "s:" + snap.SessionID
		return s.webURL("/workbench?thread=" + url.QueryEscape(thread))
	}
	return s.webURL("/jobs/" + snap.ID)
}
