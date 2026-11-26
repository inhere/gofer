package jobstore

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/util"
)

// The Team entities (design 2026-11-18 §7, plan 2026-11-26-team-plan.md §2.1): a team is one
// leader seat plus member seats, all seats of ONE project. Four tables hold it: teams,
// team_members, team_routes (one row per routing decision) and team_evals (one row per leader
// evaluation). Routes and evals are append-only.
//
// Rows are facts and constraints only; the roster (state, waiting …) is derived at read time by
// internal/team from the seat service.

// Team member roles.
const TeamRoleMember = "member"

// Team relay modes: leader (every member-to-member relay is handed to the leader; the only one
// implemented) and direct (reserved).
const (
	TeamRelayLeader = "leader"
	TeamRelayDirect = "direct"
)

// Defaults of a new team.
const (
	DefaultTeamRelayRoundLimit = 3
	DefaultTeamRelayDailyLimit = 20
	DefaultTeamMergeWindowSec  = 120
)

// Outcomes of a routing decision (team_routes.outcome).
const (
	// TeamOutcomePending is written before delivery and settled afterwards; TeamOutcomeUnknown
	// marks a row whose delivery result could not be determined.
	TeamOutcomePending        = "pending"
	TeamOutcomeUnknown        = "unknown"
	TeamOutcomeDelivered      = "delivered"
	TeamOutcomeQueued         = "queued"
	TeamOutcomeMerged         = "merged"
	TeamOutcomeHandedToLeader = "handed_to_leader"
	TeamOutcomeDroppedSelf    = "dropped_self"
	TeamOutcomeDup            = "dup"
	TeamOutcomeHeld           = "held"
)

// ValidTeamOutcome reports whether o is a routing outcome.
func ValidTeamOutcome(o string) bool {
	switch o {
	case TeamOutcomePending, TeamOutcomeUnknown, TeamOutcomeDelivered, TeamOutcomeQueued, TeamOutcomeMerged, TeamOutcomeHandedToLeader,
		TeamOutcomeDroppedSelf, TeamOutcomeDup, TeamOutcomeHeld:
		return true
	}
	return false
}

// Outcomes of a leader evaluation (team_evals.outcome). missing_eval is written by the server
// for a turn that ended without one.
const (
	TeamEvalDispatched = "dispatched"
	TeamEvalIdle       = "idle"
	TeamEvalFailed     = "failed"
	TeamEvalMissing    = "missing_eval"
)

// ValidTeamEvalOutcome reports whether o is an evaluation outcome.
func ValidTeamEvalOutcome(o string) bool {
	switch o {
	case TeamEvalDispatched, TeamEvalIdle, TeamEvalFailed, TeamEvalMissing:
		return true
	}
	return false
}

var (
	// ErrTeamNotFound: no team has that id (or name).
	ErrTeamNotFound = errors.New("team not found")
	// ErrTeamInvalid wraps every malformed team input or broken membership rule.
	ErrTeamInvalid = errors.New("invalid team")
	// ErrTeamNameTaken: an active team of the project already has that name.
	ErrTeamNameTaken = errors.New("team name already in use")
	// ErrTeamSeatTaken: the seat already belongs to another active team (as leader or member).
	ErrTeamSeatTaken = errors.New("seat already belongs to a team")
	// ErrTeamArchived: the team is archived and cannot be edited.
	ErrTeamArchived = errors.New("team is archived")
)

// Team is one teams row. Timestamps are unix seconds; ArchivedAt is 0 while active.
type Team struct {
	ID         string
	CallerID   string
	ProjectKey string
	Name       string
	// LeaderSeatID is the leader seat; it cannot also be a member.
	LeaderSeatID string
	// Brief is the team description, injected to the leader only.
	Brief           string
	RelayMode       string
	RelayRoundLimit int
	RelayDailyLimit int
	MergeWindowSec  int
	ArchivedAt      int64
	CreatedAt       int64
	UpdatedAt       int64
}

// TeamMember is one team_members row.
type TeamMember struct {
	TeamID  string
	SeatID  string
	Role    string
	AddedAt int64
}

// TeamRoute is one routing decision.
type TeamRoute struct {
	ID        int64
	TeamID    string
	MailID    int64
	FromRef   string
	ToSeatIDs []string
	Rule      string
	Hop       int
	RouteKey  string
	Outcome   string
	DecidedAt int64
}

// TeamEval is one leader evaluation of a turn.
type TeamEval struct {
	ID        int64
	TeamID    string
	LeaderRef string
	TurnRef   string
	Outcome   string
	Reason    string
	At        int64
}

// TeamFilter filters ListTeams. An empty field does not filter.
type TeamFilter struct {
	CallerID        string
	ProjectKey      string
	IncludeArchived bool
}

// TeamPatch is a partial update; a nil field is left alone.
type TeamPatch struct {
	Name            *string
	LeaderSeatID    *string
	Brief           *string
	RelayMode       *string
	RelayRoundLimit *int
	RelayDailyLimit *int
	MergeWindowSec  *int
}

func newTeamID() string {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("team-%010x", time.Now().UnixNano()&0xffffffffff)
	}
	return "team-" + hex.EncodeToString(b[:])
}

const selectTeamCols = `SELECT id, caller_id, project_key, name, leader_seat_id, brief, relay_mode,
  relay_round_limit, relay_daily_limit, merge_window_sec, COALESCE(archived_at,0), created_at, updated_at FROM teams`

func scanTeam(sc rowScanner) (Team, error) {
	var t Team
	err := sc.Scan(&t.ID, &t.CallerID, &t.ProjectKey, &t.Name, &t.LeaderSeatID, &t.Brief, &t.RelayMode,
		&t.RelayRoundLimit, &t.RelayDailyLimit, &t.MergeWindowSec, &t.ArchivedAt, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func isTeamNameUniqueErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE") && strings.Contains(err.Error(), "teams.")
}

func validateTeamKnobs(mode string, round, daily, merge int) error {
	if mode != TeamRelayLeader && mode != TeamRelayDirect {
		return fmt.Errorf("%w: relay_mode must be leader or direct", ErrTeamInvalid)
	}
	if round < 0 || daily < 0 || merge < 0 {
		return fmt.Errorf("%w: relay limits and merge window cannot be negative", ErrTeamInvalid)
	}
	return nil
}

// checkTeamSeatTx validates a seat as a team participant inside tx: it exists, is active, is a
// seat of the team's project and of the team's caller, and (unless it already belongs to
// exceptTeam) is in no other active team as leader or member.
func checkTeamSeatTx(tx *sql.Tx, team Team, seatID, exceptTeam string) error {
	x, err := scanSeat(tx.QueryRow(selectSeatCols+` WHERE id = ?`, seatID))
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrSeatNotFound, seatID)
	}
	if err != nil {
		return fmt.Errorf("jobstore: read seat %q: %w", seatID, err)
	}
	if x.ArchivedAt != 0 {
		return fmt.Errorf("%w: seat %s", ErrSeatArchived, seatID)
	}
	if x.ProjectKey != team.ProjectKey {
		return fmt.Errorf("%w: seat %q belongs to project %q, not %q", ErrTeamInvalid, x.Name, x.ProjectKey, team.ProjectKey)
	}
	if x.CallerID != team.CallerID {
		return fmt.Errorf("%w: seat %q belongs to another caller", ErrTeamInvalid, x.Name)
	}
	var other string
	err = tx.QueryRow(`SELECT t.id FROM teams t WHERE t.archived_at IS NULL AND t.id <> ? AND (t.leader_seat_id = ?
  OR EXISTS (SELECT 1 FROM team_members m WHERE m.team_id = t.id AND m.seat_id = ?)) LIMIT 1`, exceptTeam, seatID, seatID).Scan(&other)
	switch {
	case err == nil:
		return fmt.Errorf("%w: seat %q is in team %s", ErrTeamSeatTaken, x.Name, other)
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("jobstore: check team seat: %w", err)
	}
	return nil
}

// CreateTeam inserts a team with its leader and (optionally) initial members, atomically. The
// zero RelayMode defaults to leader; limits are stored as given (the service applies defaults).
func (s *Store) CreateTeam(in Team, memberSeatIDs []string) (Team, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.ProjectKey = strings.TrimSpace(in.ProjectKey)
	in.LeaderSeatID = strings.TrimSpace(in.LeaderSeatID)
	if in.Name == "" || in.ProjectKey == "" || in.LeaderSeatID == "" {
		return Team{}, fmt.Errorf("%w: name, project and leader seat required", ErrTeamInvalid)
	}
	if in.RelayMode == "" {
		in.RelayMode = TeamRelayLeader
	}
	if err := validateTeamKnobs(in.RelayMode, in.RelayRoundLimit, in.RelayDailyLimit, in.MergeWindowSec); err != nil {
		return Team{}, err
	}
	now := s.unixNow()
	in.ID, in.ArchivedAt, in.CreatedAt, in.UpdatedAt = newTeamID(), 0, now, now
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Team{}, fmt.Errorf("jobstore: begin create team: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// The leader's caller is the team's owner.
	lead, err := scanSeat(tx.QueryRow(selectSeatCols+` WHERE id = ?`, in.LeaderSeatID))
	if errors.Is(err, sql.ErrNoRows) {
		return Team{}, fmt.Errorf("%w: %s", ErrSeatNotFound, in.LeaderSeatID)
	}
	if err != nil {
		return Team{}, fmt.Errorf("jobstore: read seat %q: %w", in.LeaderSeatID, err)
	}
	in.CallerID = lead.CallerID
	if err := checkTeamSeatTx(tx, in, in.LeaderSeatID, ""); err != nil {
		return Team{}, err
	}
	if _, err := tx.Exec(`INSERT INTO teams (id, caller_id, project_key, name, leader_seat_id, brief, relay_mode,
  relay_round_limit, relay_daily_limit, merge_window_sec, archived_at, created_at, updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,NULL,?,?)`, in.ID, in.CallerID, in.ProjectKey, in.Name, in.LeaderSeatID, in.Brief,
		in.RelayMode, in.RelayRoundLimit, in.RelayDailyLimit, in.MergeWindowSec, now, now); err != nil {
		if isTeamNameUniqueErr(err) {
			return Team{}, fmt.Errorf("%w: %q in project %q", ErrTeamNameTaken, in.Name, in.ProjectKey)
		}
		return Team{}, fmt.Errorf("jobstore: insert team: %w", err)
	}
	for _, sid := range memberSeatIDs {
		if err := addTeamMemberTx(tx, in, strings.TrimSpace(sid), now); err != nil {
			return Team{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Team{}, fmt.Errorf("jobstore: commit create team: %w", err)
	}
	return in, nil
}

func addTeamMemberTx(tx *sql.Tx, team Team, seatID string, now int64) error {
	if seatID == "" {
		return fmt.Errorf("%w: seat required", ErrTeamInvalid)
	}
	if seatID == team.LeaderSeatID {
		return fmt.Errorf("%w: the leader seat cannot also be a member", ErrTeamInvalid)
	}
	if err := checkTeamSeatTx(tx, team, seatID, team.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO team_members (team_id, seat_id, role, added_at) VALUES (?,?,?,?)`,
		team.ID, seatID, TeamRoleMember, now); err != nil {
		return fmt.Errorf("jobstore: add team member: %w", err)
	}
	return nil
}

// GetTeam reads one team by id. ok is false when unknown.
func (s *Store) GetTeam(id string) (Team, bool, error) {
	t, err := scanTeam(s.db.QueryRow(selectTeamCols+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return Team{}, false, nil
	}
	if err != nil {
		return Team{}, false, fmt.Errorf("jobstore: get team %q: %w", id, err)
	}
	return t, true, nil
}

// GetTeamByName reads the ACTIVE team of a project by name.
func (s *Store) GetTeamByName(projectKey, name string) (Team, bool, error) {
	t, err := scanTeam(s.db.QueryRow(selectTeamCols+` WHERE project_key = ? AND name = ? AND archived_at IS NULL`,
		strings.TrimSpace(projectKey), strings.TrimSpace(name)))
	if errors.Is(err, sql.ErrNoRows) {
		return Team{}, false, nil
	}
	if err != nil {
		return Team{}, false, fmt.Errorf("jobstore: get team %q: %w", name, err)
	}
	return t, true, nil
}

// ListTeams lists teams, oldest first.
func (s *Store) ListTeams(f TeamFilter) ([]Team, error) {
	q, args := selectTeamCols, []any{}
	var where []string
	if f.CallerID != "" {
		where, args = append(where, "caller_id = ?"), append(args, f.CallerID)
	}
	if f.ProjectKey != "" {
		where, args = append(where, "project_key = ?"), append(args, f.ProjectKey)
	}
	if !f.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := s.db.Query(q+" ORDER BY created_at ASC, id ASC", args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list teams: %w", err)
	}
	defer rows.Close()
	var out []Team
	for rows.Next() {
		t, err := scanTeam(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: list teams: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TeamOfSeat returns the active team a seat belongs to, as leader or member (ok=false: none).
func (s *Store) TeamOfSeat(seatID string) (Team, bool, error) {
	seatID = strings.TrimSpace(seatID)
	t, err := scanTeam(s.db.QueryRow(selectTeamCols+` WHERE archived_at IS NULL AND (leader_seat_id = ?
  OR id IN (SELECT team_id FROM team_members WHERE seat_id = ?)) LIMIT 1`, seatID, seatID))
	if errors.Is(err, sql.ErrNoRows) {
		return Team{}, false, nil
	}
	if err != nil {
		return Team{}, false, fmt.Errorf("jobstore: team of seat %q: %w", seatID, err)
	}
	return t, true, nil
}

// UpdateTeam applies a patch (name, leader, brief, limits). A new leader must be a valid seat of
// the team that is not already a member of it (remove it first).
func (s *Store) UpdateTeam(id string, p TeamPatch) (Team, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Team{}, fmt.Errorf("jobstore: begin update team: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := scanTeam(tx.QueryRow(selectTeamCols+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return Team{}, ErrTeamNotFound
	}
	if err != nil {
		return Team{}, fmt.Errorf("jobstore: read team %q: %w", id, err)
	}
	if cur.ArchivedAt != 0 {
		return Team{}, ErrTeamArchived
	}
	next := cur
	if p.Name != nil {
		next.Name = strings.TrimSpace(*p.Name)
		if next.Name == "" {
			return Team{}, fmt.Errorf("%w: name required", ErrTeamInvalid)
		}
	}
	if p.Brief != nil {
		next.Brief = *p.Brief
	}
	if p.RelayMode != nil {
		next.RelayMode = *p.RelayMode
	}
	if p.RelayRoundLimit != nil {
		next.RelayRoundLimit = *p.RelayRoundLimit
	}
	if p.RelayDailyLimit != nil {
		next.RelayDailyLimit = *p.RelayDailyLimit
	}
	if p.MergeWindowSec != nil {
		next.MergeWindowSec = *p.MergeWindowSec
	}
	if err := validateTeamKnobs(next.RelayMode, next.RelayRoundLimit, next.RelayDailyLimit, next.MergeWindowSec); err != nil {
		return Team{}, err
	}
	if p.LeaderSeatID != nil && strings.TrimSpace(*p.LeaderSeatID) != cur.LeaderSeatID {
		lid := strings.TrimSpace(*p.LeaderSeatID)
		if err := checkTeamSeatTx(tx, cur, lid, cur.ID); err != nil {
			return Team{}, err
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM team_members WHERE team_id = ? AND seat_id = ?`, cur.ID, lid).Scan(&n); err != nil {
			return Team{}, fmt.Errorf("jobstore: check leader: %w", err)
		}
		if n > 0 {
			return Team{}, fmt.Errorf("%w: the leader seat cannot also be a member", ErrTeamInvalid)
		}
		next.LeaderSeatID = lid
	}
	now := s.unixNow()
	if _, err := tx.Exec(`UPDATE teams SET name=?, leader_seat_id=?, brief=?, relay_mode=?, relay_round_limit=?,
  relay_daily_limit=?, merge_window_sec=?, updated_at=? WHERE id=?`, next.Name, next.LeaderSeatID, next.Brief,
		next.RelayMode, next.RelayRoundLimit, next.RelayDailyLimit, next.MergeWindowSec, now, cur.ID); err != nil {
		if isTeamNameUniqueErr(err) {
			return Team{}, fmt.Errorf("%w: %q in project %q", ErrTeamNameTaken, next.Name, next.ProjectKey)
		}
		return Team{}, fmt.Errorf("jobstore: update team %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return Team{}, fmt.Errorf("jobstore: commit update team: %w", err)
	}
	next.UpdatedAt = now
	return next, nil
}

// ArchiveTeam sets archived_at (idempotent). Member rows stay as history; the team stops
// counting for a seat's one-team rule and frees its name.
func (s *Store) ArchiveTeam(id string) (Team, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	cur, err := scanTeam(s.db.QueryRow(selectTeamCols+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return Team{}, ErrTeamNotFound
	}
	if err != nil {
		return Team{}, fmt.Errorf("jobstore: read team %q: %w", id, err)
	}
	if cur.ArchivedAt != 0 {
		return cur, nil
	}
	now := s.unixNow()
	if _, err := s.db.Exec(`UPDATE teams SET archived_at=?, updated_at=? WHERE id=?`, now, now, cur.ID); err != nil {
		return Team{}, fmt.Errorf("jobstore: archive team %q: %w", id, err)
	}
	cur.ArchivedAt, cur.UpdatedAt = now, now
	return cur, nil
}

// AddTeamMember adds a seat to an active team (idempotent for a seat already in it).
func (s *Store) AddTeamMember(teamID, seatID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("jobstore: begin add team member: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	t, err := scanTeam(tx.QueryRow(selectTeamCols+` WHERE id = ?`, strings.TrimSpace(teamID)))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTeamNotFound
	}
	if err != nil {
		return fmt.Errorf("jobstore: read team %q: %w", teamID, err)
	}
	if t.ArchivedAt != 0 {
		return ErrTeamArchived
	}
	if err := addTeamMemberTx(tx, t, strings.TrimSpace(seatID), s.unixNow()); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveTeamMember removes a seat from a team; ok is false when it was not a member.
func (s *Store) RemoveTeamMember(teamID, seatID string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	t, ok, err := s.GetTeam(teamID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrTeamNotFound
	}
	if t.ArchivedAt != 0 {
		return false, ErrTeamArchived
	}
	res, err := s.db.Exec(`DELETE FROM team_members WHERE team_id = ? AND seat_id = ?`, t.ID, strings.TrimSpace(seatID))
	if err != nil {
		return false, fmt.Errorf("jobstore: remove team member: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListTeamMembers returns a team's members, oldest first.
func (s *Store) ListTeamMembers(teamID string) ([]TeamMember, error) {
	rows, err := s.db.Query(`SELECT team_id, seat_id, role, added_at FROM team_members WHERE team_id = ? ORDER BY added_at, seat_id`, strings.TrimSpace(teamID))
	if err != nil {
		return nil, fmt.Errorf("jobstore: list team members: %w", err)
	}
	defer rows.Close()
	var out []TeamMember
	for rows.Next() {
		var m TeamMember
		if err := rows.Scan(&m.TeamID, &m.SeatID, &m.Role, &m.AddedAt); err != nil {
			return nil, fmt.Errorf("jobstore: list team members: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AppendTeamRoute records one routing decision and returns it with its id.
func (s *Store) AppendTeamRoute(r TeamRoute) (TeamRoute, error) {
	if strings.TrimSpace(r.TeamID) == "" || !ValidTeamOutcome(r.Outcome) {
		return TeamRoute{}, fmt.Errorf("%w: route needs a team and a known outcome", ErrTeamInvalid)
	}
	if r.Hop < 0 {
		return TeamRoute{}, fmt.Errorf("%w: negative hop", ErrTeamInvalid)
	}
	if r.DecidedAt == 0 {
		r.DecidedAt = s.unixNow()
	}
	ids, err := json.Marshal(append([]string{}, r.ToSeatIDs...))
	if err != nil {
		return TeamRoute{}, fmt.Errorf("jobstore: encode route seats: %w", err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`INSERT INTO team_routes (team_id, mail_id, from_ref, to_seat_ids, rule, hop, route_key, outcome, decided_at)
  VALUES (?,?,?,?,?,?,?,?,?)`, r.TeamID, r.MailID, r.FromRef, string(ids), r.Rule, r.Hop, r.RouteKey, r.Outcome, r.DecidedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") && strings.Contains(err.Error(), "route_key") {
			return TeamRoute{}, fmt.Errorf("%w: %q", ErrTeamRouteKeyTaken, r.RouteKey)
		}
		return TeamRoute{}, fmt.Errorf("jobstore: append team route: %w", err)
	}
	r.ID, _ = res.LastInsertId()
	return r, nil
}

// ErrTeamRouteKeyTaken: a route with that (non-empty) route_key already exists for the team.
var ErrTeamRouteKeyTaken = errors.New("team route key already recorded")

// SettleTeamRoute sets the outcome of a route row (a pending row is settled after delivery).
// ok is false when the id is unknown.
func (s *Store) SettleTeamRoute(id int64, outcome string) (bool, error) {
	if !ValidTeamOutcome(outcome) {
		return false, fmt.Errorf("%w: unknown route outcome %q", ErrTeamInvalid, outcome)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE team_routes SET outcome=? WHERE id=?`, outcome, id)
	if err != nil {
		return false, fmt.Errorf("jobstore: settle team route: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SettleTeamRouteMail points a route row at the letter it produced and sets its outcome (the
// pending row is written before the letter exists). ok is false when the id is unknown.
func (s *Store) SettleTeamRouteMail(id, mailID int64, outcome string) (bool, error) {
	if !ValidTeamOutcome(outcome) || mailID < 0 {
		return false, fmt.Errorf("%w: unknown route outcome %q", ErrTeamInvalid, outcome)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE team_routes SET mail_id=?, outcome=? WHERE id=?`, mailID, outcome, id)
	if err != nil {
		return false, fmt.Errorf("jobstore: settle team route: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SettleTeamRoutesOfMail sets the outcome of the routing rows of one letter that are still in
// one of the `from` outcomes (a letter absorbed into another letter's turn becomes merged).
func (s *Store) SettleTeamRoutesOfMail(mailID int64, outcome string, from ...string) (int64, error) {
	if !ValidTeamOutcome(outcome) || len(from) == 0 {
		return 0, fmt.Errorf("%w: unknown route outcome %q", ErrTeamInvalid, outcome)
	}
	args := make([]any, 0, util.CapSum(len(from), 2))
	args = append(args, outcome, mailID)
	for _, f := range from {
		args = append(args, f)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE team_routes SET outcome=? WHERE mail_id=? AND outcome IN (`+mailPlaceholders(len(from))+`)`, args...)
	if err != nil {
		return 0, fmt.Errorf("jobstore: settle team routes of mail %d: %w", mailID, err)
	}
	return res.RowsAffected()
}

// MarkStaleTeamRoutesUnknown turns pending rows decided before `before` (unix seconds) that
// never got a letter into unknown: the process that wrote them died or lost the answer, so
// whether the letter exists can only be told by looking the route key up again.
func (s *Store) MarkStaleTeamRoutesUnknown(before int64) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE team_routes SET outcome=? WHERE outcome=? AND mail_id=0 AND decided_at<?`,
		TeamOutcomeUnknown, TeamOutcomePending, before)
	if err != nil {
		return 0, fmt.Errorf("jobstore: mark stale team routes: %w", err)
	}
	return res.RowsAffected()
}

// ListTeamRoutesSince lists a team's routing decisions decided at or after since (unix
// seconds), newest first, at most limit (<=0: 1000).
func (s *Store) ListTeamRoutesSince(teamID string, since int64, limit int) ([]TeamRoute, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.Query(`SELECT id, team_id, mail_id, from_ref, to_seat_ids, rule, hop, route_key, outcome, decided_at
  FROM team_routes WHERE team_id = ? AND decided_at >= ? ORDER BY id DESC LIMIT ?`, strings.TrimSpace(teamID), since, limit)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list team routes since: %w", err)
	}
	defer rows.Close()
	var out []TeamRoute
	for rows.Next() {
		var r TeamRoute
		var ids string
		if err := rows.Scan(&r.ID, &r.TeamID, &r.MailID, &r.FromRef, &ids, &r.Rule, &r.Hop, &r.RouteKey, &r.Outcome, &r.DecidedAt); err != nil {
			return nil, fmt.Errorf("jobstore: list team routes since: %w", err)
		}
		_ = json.Unmarshal([]byte(ids), &r.ToSeatIDs)
		out = append(out, r)
	}
	return out, rows.Err()
}

// TeamRouteByKey reads the route a team recorded under route_key (ok=false: none).
func (s *Store) TeamRouteByKey(teamID, routeKey string) (TeamRoute, bool, error) {
	rows, err := s.db.Query(`SELECT id, team_id, mail_id, from_ref, to_seat_ids, rule, hop, route_key, outcome, decided_at
  FROM team_routes WHERE team_id = ? AND route_key = ? AND route_key <> '' LIMIT 1`, strings.TrimSpace(teamID), routeKey)
	if err != nil {
		return TeamRoute{}, false, fmt.Errorf("jobstore: team route by key: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return TeamRoute{}, false, rows.Err()
	}
	var r TeamRoute
	var ids string
	if err := rows.Scan(&r.ID, &r.TeamID, &r.MailID, &r.FromRef, &ids, &r.Rule, &r.Hop, &r.RouteKey, &r.Outcome, &r.DecidedAt); err != nil {
		return TeamRoute{}, false, fmt.Errorf("jobstore: team route by key: %w", err)
	}
	_ = json.Unmarshal([]byte(ids), &r.ToSeatIDs)
	return r, true, nil
}

// ListTeamRoutes returns a team's routing decisions, newest first, at most limit (<=0: 100).
func (s *Store) ListTeamRoutes(teamID string, limit int) ([]TeamRoute, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, team_id, mail_id, from_ref, to_seat_ids, rule, hop, route_key, outcome, decided_at
  FROM team_routes WHERE team_id = ? ORDER BY id DESC LIMIT ?`, strings.TrimSpace(teamID), limit)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list team routes: %w", err)
	}
	defer rows.Close()
	var out []TeamRoute
	for rows.Next() {
		var r TeamRoute
		var ids string
		if err := rows.Scan(&r.ID, &r.TeamID, &r.MailID, &r.FromRef, &ids, &r.Rule, &r.Hop, &r.RouteKey, &r.Outcome, &r.DecidedAt); err != nil {
			return nil, fmt.Errorf("jobstore: list team routes: %w", err)
		}
		_ = json.Unmarshal([]byte(ids), &r.ToSeatIDs)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AppendTeamEval records one leader evaluation and returns it with its id.
func (s *Store) AppendTeamEval(e TeamEval) (TeamEval, error) {
	if strings.TrimSpace(e.TeamID) == "" || !ValidTeamEvalOutcome(e.Outcome) {
		return TeamEval{}, fmt.Errorf("%w: eval needs a team and a known outcome", ErrTeamInvalid)
	}
	if e.At == 0 {
		e.At = s.unixNow()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`INSERT INTO team_evals (team_id, leader_ref, turn_ref, outcome, reason, at) VALUES (?,?,?,?,?,?)`,
		e.TeamID, e.LeaderRef, e.TurnRef, e.Outcome, e.Reason, e.At)
	if err != nil {
		return TeamEval{}, fmt.Errorf("jobstore: append team eval: %w", err)
	}
	e.ID, _ = res.LastInsertId()
	return e, nil
}

// ListTeamEvals returns a team's evaluations, newest first, at most limit (<=0: 100).
func (s *Store) ListTeamEvals(teamID string, limit int) ([]TeamEval, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, team_id, leader_ref, turn_ref, outcome, reason, at FROM team_evals
  WHERE team_id = ? ORDER BY id DESC LIMIT ?`, strings.TrimSpace(teamID), limit)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list team evals: %w", err)
	}
	defer rows.Close()
	var out []TeamEval
	for rows.Next() {
		var e TeamEval
		if err := rows.Scan(&e.ID, &e.TeamID, &e.LeaderRef, &e.TurnRef, &e.Outcome, &e.Reason, &e.At); err != nil {
			return nil, fmt.Errorf("jobstore: list team evals: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// FindSeatByName returns the active seat of a project by name. callerID "" matches any caller.
func (s *Store) FindSeatByName(callerID, projectKey, name string) (Seat, bool, error) {
	q, args := selectSeatCols+` WHERE project_key = ? AND name = ? AND archived_at IS NULL`, []any{strings.TrimSpace(projectKey), strings.TrimSpace(name)}
	if callerID != "" {
		q, args = q+` AND caller_id = ?`, append(args, callerID)
	}
	x, err := scanSeat(s.db.QueryRow(q+` ORDER BY created_at LIMIT 1`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Seat{}, false, nil
	}
	if err != nil {
		return Seat{}, false, fmt.Errorf("jobstore: find seat %q: %w", name, err)
	}
	return x, true, nil
}
