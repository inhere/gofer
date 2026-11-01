package jobstore

import (
	"errors"
	"fmt"
	"strings"
)

// SummarizerSession is the work summarizer's per-project session book (gofer-ayu0).
// Instead of opening a new agent session for every tidy-up, the summarizer keeps ONE
// session per project and continues it, rotating to a fresh one once it has served
// Runs tidy-ups or is older than the configured age. The record is what survives a
// server restart: SessionID is the agent-side id the runs are pinned to, LastJobID the
// job whose session can be continued ("" = the next run must start a new session).
//
// The record now lives in the common background session table (kind 'summarizer'); the
// functions below are the per-project view of it, so the summarizer's own seam did not
// have to change.
type SummarizerSession struct {
	ProjectKey string
	SessionID  string
	LastJobID  string
	Runs       int
	CreatedAt  int64
	UpdatedAt  int64
}

// BackgroundKindSummarizer is the background_agent_sessions.kind of the work summarizer.
const BackgroundKindSummarizer = "summarizer"

func summarizerFromBackground(b BackgroundSession) SummarizerSession {
	id := b.SessionID
	if strings.HasPrefix(id, BackgroundLocalSessionPrefix) {
		id = "" // made up for a run whose agent reported none
	}
	return SummarizerSession{ProjectKey: b.Project, SessionID: id, LastJobID: b.LastJobID, Runs: b.Runs,
		CreatedAt: b.CreatedAt, UpdatedAt: b.LastUsedAt}
}

// GetSummarizerSession reads a project's summarizer session (false = never recorded).
func (s *Store) GetSummarizerSession(project string) (SummarizerSession, bool, error) {
	b, ok, err := s.oneBackgroundSession(`WHERE kind=? AND project=? AND state='active' AND manual_reset=0`,
		BackgroundKindSummarizer, project)
	if err != nil {
		return SummarizerSession{}, false, fmt.Errorf("jobstore: get summarizer session: %w", err)
	}
	if !ok {
		return SummarizerSession{}, false, nil
	}
	return summarizerFromBackground(b), true, nil
}

// SaveSummarizerSession writes (or replaces) the record of a project's summarizer
// session. An empty ProjectKey is refused: there is no meaningful global record.
func (s *Store) SaveSummarizerSession(r SummarizerSession) error {
	if r.ProjectKey == "" {
		return errors.New("jobstore: save summarizer session: an empty project key")
	}
	id := r.SessionID
	if id == "" {
		id = BackgroundLocalSessionPrefix + BackgroundKindSummarizer + ":" + r.ProjectKey
	}
	// Keep the agent / model the project's active row is bound to (a legacy save knows
	// neither), so saving does not unbind it.
	var agent, model string
	if cur, ok, err := s.oneBackgroundSession(`WHERE kind=? AND project=? AND state='active' AND manual_reset=0`,
		BackgroundKindSummarizer, r.ProjectKey); err == nil && ok {
		agent, model = cur.Agent, cur.Model
	}
	if err := s.ReplaceBackgroundSession(BackgroundSession{Kind: BackgroundKindSummarizer, SessionID: id,
		Agent: agent, Project: r.ProjectKey, Model: model, CreatedAt: r.CreatedAt, LastUsedAt: r.UpdatedAt,
		Runs: r.Runs, LastJobID: r.LastJobID}); err != nil {
		return fmt.Errorf("jobstore: save summarizer session: %w", err)
	}
	return nil
}

// DeleteSummarizerSession forgets a project's summarizer session, so the next tidy-up
// opens a fresh one.
func (s *Store) DeleteSummarizerSession(project string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`UPDATE background_agent_sessions SET state='dropped'
  WHERE kind=? AND project=? AND state='active'`, BackgroundKindSummarizer, project); err != nil {
		return fmt.Errorf("jobstore: delete summarizer session: %w", err)
	}
	return nil
}

// ListSummarizerSessionIDs returns every session id the summarizer currently holds
// (the ones a session list must hide while they belong to a tidy-up).
func (s *Store) ListSummarizerSessionIDs() ([]string, error) {
	rows, err := s.db.Query(`SELECT session_id FROM background_agent_sessions
  WHERE kind=? AND state='active' AND session_id NOT LIKE ?`, BackgroundKindSummarizer, BackgroundLocalSessionPrefix+"%")
	if err != nil {
		return nil, fmt.Errorf("jobstore: list summarizer session ids: %w", err)
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("jobstore: scan summarizer session id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list summarizer session ids rows: %w", err)
	}
	return out, nil
}
