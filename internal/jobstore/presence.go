package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// PresenceRecord is the SQLite-persisted projection of one driver-agent presence
// row (E36, design §9). Like JobRecord/InteractionRecord it is a neutral struct,
// decoupled from internal/presence, so the agent_presence table can be populated
// without this package importing presence (which would form a presence ->
// jobstore -> presence cycle).
//
// AgentToken is the软隔离 secret the agent presents on inbox/deregister ops; it is
// stored here and compared in-process by presence.Service (not a real auth). The
// nullable columns (role/project_key/caller_id/client/meta_json) COALESCE to ""
// on read (see selectPresenceCols).
type PresenceRecord struct {
	AgentID      string
	AgentToken   string
	Name         string
	Role         string
	ProjectKey   string
	CallerID     string
	Client       string
	Status       string
	RegisteredAt int64
	LastSeenAt   int64
	MetaJSON     string
}

// selectPresenceCols is the shared projection for presence reads. COALESCE guards
// the nullable columns so a NULL scans into "" instead of failing, mirroring
// jobs.selectCols / interactions.selectInterCols.
const selectPresenceCols = `SELECT agent_id, agent_token, name, COALESCE(role,''),
  COALESCE(project_key,''), COALESCE(caller_id,''), COALESCE(client,''), status,
  registered_at, last_seen_at, COALESCE(meta_json,'')
  FROM agent_presence`

// scanPresence reads one row (in selectPresenceCols order) into a PresenceRecord.
func scanPresence(sc rowScanner) (PresenceRecord, error) {
	var r PresenceRecord
	err := sc.Scan(
		&r.AgentID, &r.AgentToken, &r.Name, &r.Role,
		&r.ProjectKey, &r.CallerID, &r.Client, &r.Status,
		&r.RegisteredAt, &r.LastSeenAt, &r.MetaJSON,
	)
	return r, err
}

// UpsertPresence inserts a presence row or updates the existing one with the same
// agent_id. Register (new) and续约 (re-register / heartbeat) for one agent are two
// upserts on the same row, so the registry keeps a single, latest row per agent.
// Writes go through s.writeMu (like every other writer) so SQLite never sees two
// concurrent writers.
func (s *Store) UpsertPresence(rec PresenceRecord) error {
	if rec.AgentID == "" {
		return errors.New("jobstore: UpsertPresence: empty agent id")
	}
	if rec.AgentToken == "" {
		return errors.New("jobstore: UpsertPresence: empty agent token")
	}
	const q = `INSERT INTO agent_presence
  (agent_id, agent_token, name, role, project_key, caller_id, client, status,
   registered_at, last_seen_at, meta_json)
  VALUES (?,?,?,?,?,?,?,?,?,?,?)
  ON CONFLICT(agent_id) DO UPDATE SET
    agent_token=excluded.agent_token,
    name=excluded.name,
    role=excluded.role,
    project_key=excluded.project_key,
    caller_id=excluded.caller_id,
    client=excluded.client,
    status=excluded.status,
    last_seen_at=excluded.last_seen_at,
    meta_json=excluded.meta_json`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(q,
		rec.AgentID, rec.AgentToken, rec.Name, rec.Role, rec.ProjectKey,
		rec.CallerID, rec.Client, rec.Status,
		rec.RegisteredAt, rec.LastSeenAt, rec.MetaJSON,
	)
	if err != nil {
		return fmt.Errorf("jobstore: upsert presence %q: %w", rec.AgentID, err)
	}
	return nil
}

// GetPresence returns the presence row by agent_id. The bool is false (with a nil
// error) when no such agent exists, distinguishing "not found" from a query error
// (mirrors GetJob).
func (s *Store) GetPresence(agentID string) (PresenceRecord, bool, error) {
	rec, err := scanPresence(s.db.QueryRow(selectPresenceCols+" WHERE agent_id = ?", agentID))
	if errors.Is(err, sql.ErrNoRows) {
		return PresenceRecord{}, false, nil
	}
	if err != nil {
		return PresenceRecord{}, false, fmt.Errorf("jobstore: get presence %q: %w", agentID, err)
	}
	return rec, true, nil
}

// ListPresence returns all presence rows, most-recently-seen first. An empty
// registry yields an empty slice and no error. Online/offline is computed by the
// caller (presence.Service) from last_seen_at vs the TTL, not filtered here.
func (s *Store) ListPresence() ([]PresenceRecord, error) {
	rows, err := s.db.Query(selectPresenceCols + " ORDER BY last_seen_at DESC, agent_id ASC")
	if err != nil {
		return nil, fmt.Errorf("jobstore: list presence: %w", err)
	}
	defer rows.Close()

	out := make([]PresenceRecord, 0)
	for rows.Next() {
		rec, scanErr := scanPresence(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan presence row: %w", scanErr)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list presence rows: %w", err)
	}
	return out, nil
}

// TouchPresence refreshes an agent's last_seen_at (the heartbeat written on every
// inbox poll). It is a no-op (0 rows, nil error) when the agent does not exist.
func (s *Store) TouchPresence(agentID string, ts int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec("UPDATE agent_presence SET last_seen_at = ? WHERE agent_id = ?", ts, agentID)
	if err != nil {
		return fmt.Errorf("jobstore: touch presence %q: %w", agentID, err)
	}
	return nil
}

// DeletePresence removes an agent's presence row (active deregister). It is
// idempotent: deleting an absent agent is a no-op (nil error).
func (s *Store) DeletePresence(agentID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec("DELETE FROM agent_presence WHERE agent_id = ?", agentID)
	if err != nil {
		return fmt.Errorf("jobstore: delete presence %q: %w", agentID, err)
	}
	return nil
}

// PrunePresence deletes presence rows not seen since cutoff (GC of long-offline
// agents, driven by the serve sweeper). It returns the number of rows removed.
func (s *Store) PrunePresence(cutoff int64) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec("DELETE FROM agent_presence WHERE last_seen_at < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("jobstore: prune presence: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("jobstore: prune presence rows affected: %w", err)
	}
	return int(n), nil
}
