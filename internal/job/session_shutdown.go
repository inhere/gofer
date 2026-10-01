package job

import (
	"context"
	"fmt"
)

// ShutdownResidentSessions stops local ACP processes while leaving their persisted
// jobs recoverable. It runs after HTTP admission closes and before the store closes.
func (s *Service) ShutdownResidentSessions(ctx context.Context) error {
	s.mu.Lock()
	all := make([]*jobEntry, 0, len(s.jobs))
	for _, entry := range s.jobs {
		all = append(all, entry)
	}
	s.mu.Unlock()
	entries := make([]*jobEntry, 0)
	for _, entry := range all {
		entry.mu.Lock()
		active := entry.result.Session &&
			(entry.result.Status == StatusRunning || entry.result.Status == StatusAwaitingInput)
		if active {
			entry.shutdownRequested = true
			entry.sessionEnding = true
			entries = append(entries, entry)
		}
		cancel := entry.cancel
		entry.mu.Unlock()
		if active && cancel != nil {
			cancel()
		}
	}
	for _, entry := range entries {
		select {
		case <-entry.done:
		case <-ctx.Done():
			return fmt.Errorf("shutdown ACP sessions: %w", ctx.Err())
		}
	}
	return nil
}
