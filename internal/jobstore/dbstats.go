package jobstore

import (
	"fmt"
	"os"
	"time"
)

// DBStats is a point-in-time picture of the SQLite metadata database for the web
// console's "Server DB" card: the file sizes, the page geometry and the row count
// of every table the dashboard reports. Partial=true means the row-count pass ran
// out of its budget before visiting every table — the counts gathered so far are
// still returned (the cheap file/page info is always complete).
type DBStats struct {
	Path         string
	SizeBytes    int64
	WALSizeBytes int64
	PageSize     int64
	PageCount    int64
	Tables       map[string]int64
	Partial      bool
}

// dbStatTables is the row-count probe list, in report order: the metadata tables
// the dashboard shows. A table absent from the live schema (an older db, or one
// whose migration was skipped) is left out of Tables instead of being reported as
// 0 — the keys always describe the real schema, and the card therefore cannot
// print a table that does not exist.
var dbStatTables = []string{
	"jobs",
	"job_events",
	"interactions",
	"agent_sessions",
	"plan_decisions",
	"plans",
	"plan_todos",
	"workflows",
	"schedules",
	"pty_sessions",
	"event_deliveries",
}

// DBStats reports the metadata db's file/page picture plus a COUNT(*) per reported
// table. budget caps the TOTAL row-count pass (the counts are per-table SELECTs,
// each fast, but a big or cold db must never stall the dashboard); when it is
// exhausted the remaining tables are skipped and Partial is set. The file and
// page queries always run — they are single pragmas/stat calls.
func (s *Store) DBStats(budget time.Duration) (DBStats, error) {
	out := DBStats{Path: s.path, Tables: map[string]int64{}}

	// Size picture: the db file itself plus its WAL (absent after a checkpoint or
	// on a non-WAL db — 0, not an error).
	if fi, err := os.Stat(s.path); err == nil {
		out.SizeBytes = fi.Size()
	} else if !os.IsNotExist(err) {
		return DBStats{}, fmt.Errorf("jobstore: stat db: %w", err)
	}
	if fi, err := os.Stat(s.path + "-wal"); err == nil {
		out.WALSizeBytes = fi.Size()
	} else if !os.IsNotExist(err) {
		return DBStats{}, fmt.Errorf("jobstore: stat db wal: %w", err)
	}

	for _, pragma := range []struct {
		q    string
		dst  *int64
		name string
	}{
		{"PRAGMA page_size", &out.PageSize, "page_size"},
		{"PRAGMA page_count", &out.PageCount, "page_count"},
	} {
		if err := s.db.QueryRow(pragma.q).Scan(pragma.dst); err != nil {
			return DBStats{}, fmt.Errorf("jobstore: read %s: %w", pragma.name, err)
		}
	}

	present, err := s.tableNames()
	if err != nil {
		return DBStats{}, err
	}

	start := time.Now()
	for _, table := range dbStatTables {
		if _, ok := present[table]; !ok {
			continue
		}
		if time.Since(start) >= budget {
			out.Partial = true
			break
		}
		var n int64
		// Table names come from the fixed list above (never from user input), so
		// interpolation is safe here; the driver cannot bind identifiers anyway.
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			return DBStats{}, fmt.Errorf("jobstore: count %s: %w", table, err)
		}
		out.Tables[table] = n
	}
	return out, nil
}

// tableNames lists the tables of the live schema.
func (s *Store) tableNames() (map[string]struct{}, error) {
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("jobstore: scan table name: %w", err)
		}
		out[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list tables: %w", err)
	}
	return out, nil
}

// SessionStats is the agent-session aggregate behind the dashboard's "Sessions"
// card (session relay, SESS-01 §5): how many terminal sessions the hub knows, how
// they split by state and by relay mode, how many relay turns still wait for a
// human answer, and how many sessions were seen in the last hour.
type SessionStats struct {
	Total        int
	ByState      map[string]int
	ByRelayMode  map[string]int
	WaitingTurns int
	SeenWithin1h int
}

// seenWithinWindowSec is the "recently active" window the dashboard reports.
const seenWithinWindowSec = 3600

// SessionStats aggregates agent_sessions and their relay turns. now is the current
// unix seconds. Unlike the decision READ paths it performs NO lazy expiry (a
// dashboard poll must not write): a relay turn counts as waiting only while it is
// OPEN *and* still inside its own timeout window, which is the same set the expiry
// sweep would keep. ByState/ByRelayMode are zero-filled for every known state/mode
// so the JSON shape is stable; unknown values a newer hook wrote still appear.
func (s *Store) SessionStats(now int64) (SessionStats, error) {
	out := SessionStats{
		ByState:     map[string]int{},
		ByRelayMode: map[string]int{},
	}
	for _, st := range []string{SessionRunning, SessionIdle, SessionWaitingReply, SessionNeedsAttention, SessionEnded} {
		out.ByState[st] = 0
	}
	for _, m := range []string{RelayModeAuto, RelayModeOn, RelayModeOff} {
		out.ByRelayMode[m] = 0
	}

	byState, total, err := s.countBy(`SELECT state, COUNT(*) FROM agent_sessions GROUP BY state`)
	if err != nil {
		return SessionStats{}, err
	}
	for k, v := range byState {
		out.ByState[k] = v
	}
	out.Total = total

	byMode, _, err := s.countBy(`SELECT COALESCE(relay_mode, ?), COUNT(*) FROM agent_sessions GROUP BY 1`, RelayModeAuto)
	if err != nil {
		return SessionStats{}, err
	}
	for k, v := range byMode {
		out.ByRelayMode[k] = v
	}

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM plan_decisions
  WHERE kind = ? AND state = ? AND asked_at + timeout_sec > ?`,
		DecisionKindRelay, DecisionOpen, now).Scan(&out.WaitingTurns); err != nil {
		return SessionStats{}, fmt.Errorf("jobstore: count waiting relay turns: %w", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM agent_sessions WHERE last_seen_at >= ?`,
		now-seenWithinWindowSec).Scan(&out.SeenWithin1h); err != nil {
		return SessionStats{}, fmt.Errorf("jobstore: count recently seen sessions: %w", err)
	}
	return out, nil
}

// countBy runs a two-column GROUP BY query and returns the groups plus the sum of
// their counts (one pass, so callers need no second COUNT(*)).
func (s *Store) countBy(q string, args ...any) (map[string]int, int, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("jobstore: group count: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]int{}
	total := 0
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			return nil, 0, fmt.Errorf("jobstore: scan group count: %w", err)
		}
		out[key] = n
		total += n
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("jobstore: group count: %w", err)
	}
	return out, total, nil
}
