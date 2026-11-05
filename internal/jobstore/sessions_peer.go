package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/config"
)

// The session peer address (design 2026-11-05). A Claude Code session's messaging
// endpoint is unique while the process lives, but the OS re-uses a dead process's
// pid — and with it the same socket path — for a new session. A row that keeps such
// an address would make gofer deliver to whoever now owns that path, so a write that
// stores an address clears it from the other rows of the same runner that still hold
// it (design 「地址更新」). This file holds that rule and the two unbounded lookups the
// delivery decision needs; the write paths themselves stay in sessions.go.

// writeSessionRow runs one agent-sessions INSERT/UPDATE that also carries a peer
// address. socket is the address THIS write reported (not the value the statement
// stores: a write that carried no address must leave the other rows alone), and
// runner is the label the row is left with. When socket is non-empty the statement
// and the clean-up of the other rows run in ONE transaction; otherwise it is the
// plain single statement it was before. The caller holds writeMu.
func (s *Store) writeSessionRow(sid, socket, runner, q string, args []any) (int64, error) {
	if strings.TrimSpace(socket) == "" {
		res, err := s.db.Exec(q, args...)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		return n, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("jobstore: begin agent session write %q: %w", sid, err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := clearPeerSocketOthersTx(tx, sid, socket, runner); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("jobstore: commit agent session write %q: %w", sid, err)
	}
	return n, nil
}

// touchPeerSocketLocked applies a heartbeat that stores an address: the UPDATE q and
// the clean-up of the other rows run in one transaction, and the runner the row
// already has is read INSIDE it (a beat carries no runner, and the comparison must
// use the stored label). It answers the number of rows the UPDATE changed; an
// unknown session changes nothing (0, nil) so the hook's register-then-retry keeps
// working exactly as before. The caller holds writeMu.
func (s *Store) touchPeerSocketLocked(sid, socket, q string, args []any) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("jobstore: begin peer address beat %q: %w", sid, err)
	}
	defer func() { _ = tx.Rollback() }()
	var runner string
	err = tx.QueryRow(`SELECT COALESCE(runner,'') FROM agent_sessions WHERE session_id=?`, sid).Scan(&runner)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("jobstore: read runner of agent session %q: %w", sid, err)
	}
	res, err := tx.Exec(q, args...)
	if err != nil {
		return 0, fmt.Errorf("jobstore: write peer address of agent session %q: %w", sid, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n > 0 {
		if err := clearPeerSocketOthersTx(tx, sid, socket, runner); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("jobstore: commit peer address beat %q: %w", sid, err)
	}
	return n, nil
}

// clearPeerSocketOthersTx clears a peer address from the other rows that still hold
// it under the same runner (design 「地址更新」). Those rows lose the address AND
// their peer_messaging flag: a row that keeps `peer_messaging` while its address is
// gone would look deliverable by name — and if a same-name session exists, the
// delivery would land in the wrong one. A row that is still alive restores both on
// its next heartbeat.
//
// The rows are found by the ADDRESS (only a row that holds this very address can
// have had it re-used) and then compared in Go, because the runner is stored
// verbatim: an old row may spell the built-in runner the other way round (G043).
// A row without a runner does not take part: it never said where it runs, so it
// must not strip an address from a runner-labelled row (plan R2).
func clearPeerSocketOthersTx(tx *sql.Tx, sid, socket, runner string) error {
	if strings.TrimSpace(socket) == "" || strings.TrimSpace(runner) == "" {
		return nil
	}
	rows, err := tx.Query(`SELECT session_id, COALESCE(runner,'') FROM agent_sessions
  WHERE session_id<>? AND COALESCE(peer_socket,'')=?`, sid, socket)
	if err != nil {
		return fmt.Errorf("jobstore: list the sessions holding peer address: %w", err)
	}
	var others []string
	for rows.Next() {
		var otherID, otherRunner string
		if scanErr := rows.Scan(&otherID, &otherRunner); scanErr != nil {
			_ = rows.Close()
			return fmt.Errorf("jobstore: scan the sessions holding peer address: %w", scanErr)
		}
		if config.SameRunnerLabel(otherRunner, runner) {
			others = append(others, otherID)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("jobstore: list the sessions holding peer address rows: %w", err)
	}
	_ = rows.Close()
	for _, otherID := range others {
		if _, err := tx.Exec(`UPDATE agent_sessions SET peer_socket='', peer_messaging=0
  WHERE session_id=?`, otherID); err != nil {
			return fmt.Errorf("jobstore: clear peer address of session %q: %w", otherID, err)
		}
	}
	return nil
}

// ListLiveSessionsByPeerName returns every session that has not ended and carries
// this peer name, internal sessions included and with no row limit: the delivery
// decision asks "is there ANOTHER live session with this name", and a paged list
// (ListAgentSessions' default 200) could answer that question wrongly (plan R3).
func (s *Store) ListLiveSessionsByPeerName(name string) ([]AgentSession, error) {
	if strings.TrimSpace(name) == "" {
		return nil, nil
	}
	return s.queryAgentSessions(selectSessionCols+` WHERE state<>? AND COALESCE(peer_name,'')=?
  ORDER BY session_id ASC`, SessionEnded, name)
}

// ListLiveSessionsByPeerSocket returns every session that has not ended and holds
// this peer address. A messenger's reply names the address it was sent to, and more
// than one live row can hold it (a stale row keeps it until its next heartbeat), so
// the caller decides which of them — if any — the reply belongs to.
func (s *Store) ListLiveSessionsByPeerSocket(socket string) ([]AgentSession, error) {
	if strings.TrimSpace(socket) == "" {
		return nil, nil
	}
	return s.queryAgentSessions(selectSessionCols+` WHERE state<>? AND COALESCE(peer_socket,'')=?
  ORDER BY session_id ASC`, SessionEnded, socket)
}

// queryAgentSessions runs a selectSessionCols query and scans every row.
func (s *Store) queryAgentSessions(q string, args ...any) ([]AgentSession, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: query agent sessions: %w", err)
	}
	defer rows.Close()
	out := make([]AgentSession, 0)
	for rows.Next() {
		a, scanErr := scanSession(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan agent session: %w", scanErr)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: query agent sessions rows: %w", err)
	}
	return out, nil
}
