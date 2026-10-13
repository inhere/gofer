package jobstore

import (
	"fmt"
	"strings"
)

// UnknownSyncClientHost is the client_host recorded when a sync request does not
// say which machine it came from.
const UnknownSyncClientHost = "unknown"

// TrackerSyncClient is one machine's last sync of a tracker: which host, under which
// authenticated caller, when, and the client's one-line summary.
type TrackerSyncClient struct {
	TrackerID  string `json:"tracker_id"`
	ClientHost string `json:"client_host"`
	Caller     string `json:"caller"`
	LastSyncAt int64  `json:"last_sync_at"`
	Summary    string `json:"summary"`
}

// RecordTrackerSyncClient upserts the (tracker, host, caller) row with the latest
// sync time and summary. An empty host is stored as UnknownSyncClientHost.
func (s *Store) RecordTrackerSyncClient(c TrackerSyncClient) error {
	if c.TrackerID == "" {
		return fmt.Errorf("tracker_id required")
	}
	host := strings.TrimSpace(c.ClientHost)
	if host == "" {
		host = UnknownSyncClientHost
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO tracker_sync_clients(tracker_id,client_host,caller,last_sync_at,summary) VALUES(?,?,?,?,?)
ON CONFLICT(tracker_id,client_host,caller) DO UPDATE SET last_sync_at=excluded.last_sync_at,summary=excluded.summary`,
		c.TrackerID, host, c.Caller, c.LastSyncAt, c.Summary)
	return err
}

// ListTrackerSyncClients lists the machines that synced trackerID, most recent first.
func (s *Store) ListTrackerSyncClients(trackerID string) ([]TrackerSyncClient, error) {
	rows, err := s.db.Query(`SELECT tracker_id,client_host,caller,last_sync_at,summary FROM tracker_sync_clients
WHERE tracker_id=? ORDER BY last_sync_at DESC, client_host, caller`, trackerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TrackerSyncClient{}
	for rows.Next() {
		var c TrackerSyncClient
		if err := rows.Scan(&c.TrackerID, &c.ClientHost, &c.Caller, &c.LastSyncAt, &c.Summary); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
