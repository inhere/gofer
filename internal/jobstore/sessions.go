package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Agent-session states (session relay, design SESS-01 §5). A session is
// registered by the agent CLI's hooks (SessionStart) and moves between states
// as hook events arrive:
//
//	running ─Stop(relay off)─▶ idle ─UserPromptSubmit─▶ running
//	running ─Stop(relay on)──▶ waiting_reply ─answer─▶ running
//	running ─Notification────▶ needs_attention (display only)
//	*       ─SessionEnd──────▶ ended
const (
	SessionRunning        = "running"
	SessionIdle           = "idle"
	SessionWaitingReply   = "waiting_reply"
	SessionNeedsAttention = "needs_attention"
	SessionEnded          = "ended"
)

// ValidSessionState reports whether s is one of the agent-session states.
func ValidSessionState(s string) bool {
	switch s {
	case SessionRunning, SessionIdle, SessionWaitingReply, SessionNeedsAttention, SessionEnded:
		return true
	}
	return false
}

// maxSessionLastMessage caps the stored last assistant message (bytes). The
// hook already truncates; this is the store-side guard so a runaway payload
// cannot bloat the row.
const maxSessionLastMessage = 8 * 1024

// AgentSession is a terminal agent CLI session (Claude Code / Codex) registered
// with the hub so the web can observe it and relay replies into it. SessionID is
// the CLI's own session id (the hook stdin `session_id`). Timestamps are unix
// seconds.
type AgentSession struct {
	SessionID   string
	Agent       string
	ProjectKey  string
	Runner      string
	Cwd         string
	Title       string
	Transcript  string
	TmuxPane    string
	State       string
	Relay       bool
	TurnNo      int64
	LastMessage string
	LastEvent   string
	LastSeenAt  int64
	StartedAt   int64
	EndedAt     int64
}

const selectSessionCols = `SELECT session_id, COALESCE(agent,''), COALESCE(project_key,''),
  COALESCE(runner,''), COALESCE(cwd,''), COALESCE(title,''), COALESCE(transcript,''),
  COALESCE(tmux_pane,''), state, relay, turn_no, COALESCE(last_message,''),
  COALESCE(last_event,''), last_seen_at, started_at, COALESCE(ended_at,0)
  FROM agent_sessions`

func scanSession(sc rowScanner) (AgentSession, error) {
	var a AgentSession
	var relay int64
	err := sc.Scan(&a.SessionID, &a.Agent, &a.ProjectKey, &a.Runner, &a.Cwd, &a.Title,
		&a.Transcript, &a.TmuxPane, &a.State, &relay, &a.TurnNo, &a.LastMessage,
		&a.LastEvent, &a.LastSeenAt, &a.StartedAt, &a.EndedAt)
	a.Relay = relay == 1
	return a, err
}

// UpsertAgentSession registers a session or refreshes an existing one. On
// insert the row starts `running` (unless in.State is given) with started_at =
// now. On update only NON-EMPTY descriptive fields overwrite the stored ones
// (agent/project/runner/cwd/title/transcript/tmux_pane), last_seen_at is
// refreshed, and an `ended` session comes back to `running` (a resumed CLI
// session re-registers under the same id). Relay and turn_no are never touched
// here. It returns the stored row.
func (s *Store) UpsertAgentSession(in AgentSession) (AgentSession, error) {
	sid := strings.TrimSpace(in.SessionID)
	if sid == "" {
		return AgentSession{}, errors.New("jobstore: UpsertAgentSession: empty session_id")
	}
	if in.State != "" && !ValidSessionState(in.State) {
		return AgentSession{}, fmt.Errorf("jobstore: UpsertAgentSession: invalid state %q", in.State)
	}
	now := s.unixNow()
	s.writeMu.Lock()
	existing, ok, err := s.getSessionLocked(sid)
	if err != nil {
		s.writeMu.Unlock()
		return AgentSession{}, err
	}
	if !ok {
		state := in.State
		if state == "" {
			state = SessionRunning
		}
		const q = `INSERT INTO agent_sessions
  (session_id, agent, project_key, runner, cwd, title, transcript, tmux_pane, state, relay,
   turn_no, last_message, last_event, last_seen_at, started_at, ended_at)
  VALUES (?,?,?,?,?,?,?,?,?,0,0,NULL,?,?,?,NULL)`
		_, err = s.db.Exec(q, sid, in.Agent, in.ProjectKey, in.Runner, in.Cwd, in.Title,
			in.Transcript, in.TmuxPane, state, in.LastEvent, now, now)
		s.writeMu.Unlock()
		if err != nil {
			return AgentSession{}, fmt.Errorf("jobstore: insert agent session %q: %w", sid, err)
		}
		return s.getSession(sid)
	}
	pick := func(newV, oldV string) string {
		if strings.TrimSpace(newV) != "" {
			return newV
		}
		return oldV
	}
	state := existing.State
	if in.State != "" {
		state = in.State
	} else if state == SessionEnded {
		state = SessionRunning
	}
	const q = `UPDATE agent_sessions SET agent=?, project_key=?, runner=?, cwd=?, title=?,
  transcript=?, tmux_pane=?, state=?, last_event=?, last_seen_at=?, ended_at=NULL
  WHERE session_id=?`
	_, err = s.db.Exec(q, pick(in.Agent, existing.Agent), pick(in.ProjectKey, existing.ProjectKey),
		pick(in.Runner, existing.Runner), pick(in.Cwd, existing.Cwd), pick(in.Title, existing.Title),
		pick(in.Transcript, existing.Transcript), pick(in.TmuxPane, existing.TmuxPane),
		state, pick(in.LastEvent, existing.LastEvent), now, sid)
	s.writeMu.Unlock()
	if err != nil {
		return AgentSession{}, fmt.Errorf("jobstore: update agent session %q: %w", sid, err)
	}
	return s.getSession(sid)
}

// SessionHeartbeat is the per-event update carried by TouchAgentSession. Empty
// fields are left unchanged; Title only fills an EMPTY stored title (the first
// user prompt names the session, later prompts do not rename it).
type SessionHeartbeat struct {
	Event       string
	State       string
	LastMessage string
	Title       string
}

// TouchAgentSession applies a hook heartbeat: refreshes last_seen_at and
// last_event, optionally moves state and stores the latest assistant message.
// State `ended` also stamps ended_at. ok is false when the session is unknown.
func (s *Store) TouchAgentSession(sid string, hb SessionHeartbeat) (AgentSession, bool, error) {
	if hb.State != "" && !ValidSessionState(hb.State) {
		return AgentSession{}, false, fmt.Errorf("jobstore: TouchAgentSession: invalid state %q", hb.State)
	}
	msg := hb.LastMessage
	if len(msg) > maxSessionLastMessage {
		msg = msg[:maxSessionLastMessage]
	}
	now := s.unixNow()
	sets := []string{"last_seen_at=?"}
	args := []any{now}
	if hb.Event != "" {
		sets = append(sets, "last_event=?")
		args = append(args, hb.Event)
	}
	if hb.State != "" {
		sets = append(sets, "state=?")
		args = append(args, hb.State)
		if hb.State == SessionEnded {
			sets = append(sets, "ended_at=?")
			args = append(args, now)
		}
	}
	if msg != "" {
		sets = append(sets, "last_message=?")
		args = append(args, msg)
	}
	if strings.TrimSpace(hb.Title) != "" {
		sets = append(sets, "title=CASE WHEN COALESCE(title,'')='' THEN ? ELSE title END")
		args = append(args, hb.Title)
	}
	args = append(args, sid)
	s.writeMu.Lock()
	res, err := s.db.Exec("UPDATE agent_sessions SET "+strings.Join(sets, ", ")+" WHERE session_id=?", args...)
	s.writeMu.Unlock()
	if err != nil {
		return AgentSession{}, false, fmt.Errorf("jobstore: touch agent session %q: %w", sid, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return AgentSession{}, false, nil
	}
	a, err := s.getSession(sid)
	return a, err == nil, err
}

// GetAgentSession returns one session; ok is false with nil error when absent.
func (s *Store) GetAgentSession(sid string) (AgentSession, bool, error) {
	a, err := scanSession(s.db.QueryRow(selectSessionCols+" WHERE session_id = ?", sid))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentSession{}, false, nil
	}
	if err != nil {
		return AgentSession{}, false, fmt.Errorf("jobstore: get agent session %q: %w", sid, err)
	}
	return a, true, nil
}

func (s *Store) getSession(sid string) (AgentSession, error) {
	a, ok, err := s.GetAgentSession(sid)
	if err != nil {
		return AgentSession{}, err
	}
	if !ok {
		return AgentSession{}, fmt.Errorf("jobstore: agent session %q vanished", sid)
	}
	return a, nil
}

// getSessionLocked is GetAgentSession for callers already holding writeMu
// (reads do not take the lock, so this is just a naming aid for the invariant).
func (s *Store) getSessionLocked(sid string) (AgentSession, bool, error) {
	return s.GetAgentSession(sid)
}

// ListSessionsOpts filters ListAgentSessions. Cwd matches sessions whose cwd
// equals the given path OR is an ancestor of it (so `gofer session relay on`
// run from a sub-directory still finds the session started at the repo root).
// Ended sessions are excluded unless IncludeEnded. Limit <= 0 means 200.
type ListSessionsOpts struct {
	Project      string
	State        string
	Agent        string
	Cwd          string
	IncludeEnded bool
	Limit        int
}

// ListAgentSessions lists sessions, waiting_reply / needs_attention first, then
// most recently seen first.
func (s *Store) ListAgentSessions(opts ListSessionsOpts) ([]AgentSession, error) {
	if opts.State != "" && !ValidSessionState(opts.State) {
		return nil, fmt.Errorf("jobstore: list agent sessions: invalid state %q", opts.State)
	}
	var where []string
	var args []any
	if opts.Project != "" {
		where = append(where, "project_key = ?")
		args = append(args, opts.Project)
	}
	if opts.Agent != "" {
		where = append(where, "agent = ?")
		args = append(args, opts.Agent)
	}
	if opts.State != "" {
		where = append(where, "state = ?")
		args = append(args, opts.State)
	} else if !opts.IncludeEnded {
		where = append(where, "state <> 'ended'")
	}
	if cwd := strings.TrimRight(strings.TrimSpace(opts.Cwd), "/\\"); cwd != "" {
		where = append(where, "(cwd = ? OR ? LIKE cwd || '/%' OR ? LIKE cwd || '\\%')")
		args = append(args, cwd, cwd, cwd)
	}
	q := selectSessionCols
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += ` ORDER BY CASE state WHEN 'waiting_reply' THEN 0 WHEN 'needs_attention' THEN 1 ELSE 2 END,
  last_seen_at DESC, session_id ASC`
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	q += " LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list agent sessions: %w", err)
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
		return nil, fmt.Errorf("jobstore: list agent sessions rows: %w", err)
	}
	return out, nil
}

// SetSessionRelay flips the relay switch. ok is false when the session is unknown.
func (s *Store) SetSessionRelay(sid string, on bool) (bool, error) {
	relay := 0
	if on {
		relay = 1
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE agent_sessions SET relay=? WHERE session_id=?`, relay, sid)
	if err != nil {
		return false, fmt.Errorf("jobstore: set relay %q: %w", sid, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetSessionState moves a session to state (no other field changes besides
// ended_at for `ended`). ok is false when the session is unknown.
func (s *Store) SetSessionState(sid, state string) (bool, error) {
	if !ValidSessionState(state) {
		return false, fmt.Errorf("jobstore: SetSessionState: invalid state %q", state)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	q := `UPDATE agent_sessions SET state=? WHERE session_id=?`
	args := []any{state, sid}
	if state == SessionEnded {
		q = `UPDATE agent_sessions SET state=?, ended_at=? WHERE session_id=?`
		args = []any{state, s.unixNow(), sid}
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return false, fmt.Errorf("jobstore: set session state %q: %w", sid, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// IncrSessionTurn increments turn_no and returns the new value. ok is false
// when the session is unknown.
func (s *Store) IncrSessionTurn(sid string) (int64, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE agent_sessions SET turn_no = turn_no + 1 WHERE session_id=?`, sid)
	if err != nil {
		return 0, false, fmt.Errorf("jobstore: incr turn %q: %w", sid, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, false, nil
	}
	var turn int64
	if err := s.db.QueryRow(`SELECT turn_no FROM agent_sessions WHERE session_id=?`, sid).Scan(&turn); err != nil {
		return 0, false, fmt.Errorf("jobstore: read turn %q: %w", sid, err)
	}
	return turn, true, nil
}

// DeleteAgentSession removes a session registration (its relay decisions stay
// for audit). ok is false when the session is unknown.
func (s *Store) DeleteAgentSession(sid string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM agent_sessions WHERE session_id=?`, sid)
	if err != nil {
		return false, fmt.Errorf("jobstore: delete agent session %q: %w", sid, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
