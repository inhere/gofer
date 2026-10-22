package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// The resident sessions of `agent:<名字>` addresses (设计 2026-10-22 mail-agent-address「D3」):
// one row per (name, project) remembering the session job gofer started for that address, how
// many letters it took, and the session id to carry over when it has to be started again.
//
// The row is a note about gofer's own choice, never the source of truth about a job: whether
// the job still runs is the job table's answer, and a row whose job ended is simply restarted.
const (
	// MailResidentLive: the row's job is the resident session of that address.
	MailResidentLive = "resident"
	// MailResidentEnded: the session was stopped (idle timeout, rotation, or by hand).
	MailResidentEnded = "ended"
	// MailResidentFailed: the session could not be started; last_error says why.
	MailResidentFailed = "failed"
)

// MailResidentAgent is one resident session row.
type MailResidentAgent struct {
	// Name is the configured 收件 agent name (the `agent:<名字>` value).
	Name string
	// Project is the project key the session runs in ("" = the sender's default project).
	Project string
	// JobID is the session job currently (or last) serving this address.
	JobID string
	// SessionID is the ACP session id of JobID: the conversation a later job can resume.
	SessionID string
	// Letters is how many letters the CURRENT job has taken (rotation counts from its start).
	Letters int
	// CreatedAt is when the current job was started (unix seconds).
	CreatedAt int64
	// EndedAt is when the row's job stopped (0 while it is live).
	EndedAt int64
	// Status is MailResidentLive / MailResidentEnded / MailResidentFailed.
	Status string
	// LastError explains a failed start (or why the last session ended).
	LastError string
}

// ResidentKey is how a resident session is looked up: the name plus the project it serves.
func ResidentKey(name, project string) string {
	name, project = strings.TrimSpace(name), strings.TrimSpace(project)
	if project == "" {
		return name
	}
	return name + "@" + project
}

// SplitResidentKey is ResidentKey's inverse. A name cannot contain '@' (config.validate refuses
// one), so the LAST '@' separates the project.
func SplitResidentKey(key string) (name, project string) {
	key = strings.TrimSpace(key)
	if i := strings.LastIndex(key, "@"); i > 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}

// GetResidentAgent reads the row of one (name, project). ok is false when there is none.
func (s *Store) GetResidentAgent(name, project string) (MailResidentAgent, bool, error) {
	row := s.db.QueryRow(`SELECT name, project, job_id, session_id, letters, created_at, ended_at, status, last_error
  FROM mail_resident_agents WHERE name=? AND project=?`, strings.TrimSpace(name), strings.TrimSpace(project))
	a, err := scanResidentAgent(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MailResidentAgent{}, false, nil
		}
		return MailResidentAgent{}, false, fmt.Errorf("jobstore: get resident agent %s: %w", ResidentKey(name, project), err)
	}
	return a, true, nil
}

// ListResidentAgents lists the rows with the given statuses (empty = every row), newest first.
func (s *Store) ListResidentAgents(statuses ...string) ([]MailResidentAgent, error) {
	q := `SELECT name, project, job_id, session_id, letters, created_at, ended_at, status, last_error
  FROM mail_resident_agents`
	args := mailAnySlice(statuses)
	if len(statuses) > 0 {
		q += " WHERE status IN (" + mailPlaceholders(len(statuses)) + ")"
	}
	q += " ORDER BY created_at DESC, name ASC"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list resident agents: %w", err)
	}
	defer rows.Close()
	var out []MailResidentAgent
	for rows.Next() {
		a, err := scanResidentAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: list resident agents: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CountLiveResidentAgents counts the rows whose session is the live one (the agents_max_resident
// ceiling is about those).
func (s *Store) CountLiveResidentAgents() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM mail_resident_agents WHERE status=?`, MailResidentLive).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("jobstore: count resident agents: %w", err)
	}
	return n, nil
}

// PutResidentAgent writes one row (upsert on name + project).
func (s *Store) PutResidentAgent(a MailResidentAgent) error {
	a.Name, a.Project = strings.TrimSpace(a.Name), strings.TrimSpace(a.Project)
	if a.Name == "" {
		return fmt.Errorf("%w: resident agent name required", ErrMailInvalid)
	}
	if a.CreatedAt <= 0 {
		a.CreatedAt = s.unixNow()
	}
	defer s.emit(Change{Kind: ChangeMail})
	_, err := s.db.Exec(`INSERT INTO mail_resident_agents(name, project, job_id, session_id, letters, created_at, ended_at, status, last_error)
  VALUES (?,?,?,?,?,?,?,?,?)
  ON CONFLICT(name, project) DO UPDATE SET job_id=excluded.job_id, session_id=excluded.session_id,
    letters=excluded.letters, created_at=excluded.created_at, ended_at=excluded.ended_at,
    status=excluded.status, last_error=excluded.last_error`,
		a.Name, a.Project, a.JobID, a.SessionID, a.Letters, a.CreatedAt, a.EndedAt, a.Status, a.LastError)
	if err != nil {
		return fmt.Errorf("jobstore: put resident agent %s: %w", ResidentKey(a.Name, a.Project), err)
	}
	return nil
}

// SetResidentLetters records how many letters the row's current job has taken.
func (s *Store) SetResidentLetters(name, project string, letters int) error {
	_, err := s.db.Exec(`UPDATE mail_resident_agents SET letters=? WHERE name=? AND project=?`,
		letters, strings.TrimSpace(name), strings.TrimSpace(project))
	if err != nil {
		return fmt.Errorf("jobstore: set resident agent letters %s: %w", ResidentKey(name, project), err)
	}
	return nil
}

// EndResidentAgent stamps a session as ended (or failed) with the reason, keeping job_id and
// session_id so a later start can still resume the conversation.
func (s *Store) EndResidentAgent(name, project, status, reason string) error {
	if status == "" {
		status = MailResidentEnded
	}
	defer s.emit(Change{Kind: ChangeMail})
	_, err := s.db.Exec(`UPDATE mail_resident_agents SET status=?, ended_at=?, last_error=? WHERE name=? AND project=?`,
		status, s.unixNow(), strings.TrimSpace(reason), strings.TrimSpace(name), strings.TrimSpace(project))
	if err != nil {
		return fmt.Errorf("jobstore: end resident agent %s: %w", ResidentKey(name, project), err)
	}
	return nil
}

// scanResidentAgent reads one row (a *sql.Row or *sql.Rows).
func scanResidentAgent(sc rowScanner) (MailResidentAgent, error) {
	var a MailResidentAgent
	err := sc.Scan(&a.Name, &a.Project, &a.JobID, &a.SessionID, &a.Letters, &a.CreatedAt, &a.EndedAt, &a.Status, &a.LastError)
	return a, err
}
