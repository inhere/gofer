package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// The team routes (plan 2026-11-26-team-plan.md §2.1, §2.5): /v1/teams. A team is one leader seat
// plus member seats of one project; the wire types mirror internal/httpapi's team views, and the
// seat fields of a roster row are the same as in GET /v1/seats.

// Team is one team as the server reports it.
type Team struct {
	ID         string `json:"id"`
	CallerID   string `json:"caller_id,omitempty"`
	ProjectKey string `json:"project_key"`
	Name       string `json:"name"`
	// LeaderSeatID is the leader seat's id; Roster names it by seat name.
	LeaderSeatID    string `json:"leader_seat_id"`
	Brief           string `json:"brief,omitempty"`
	RelayMode       string `json:"relay_mode"`
	RelayRoundLimit int    `json:"relay_round_limit"`
	RelayDailyLimit int    `json:"relay_daily_limit"`
	MergeWindowSec  int    `json:"merge_window_sec"`
	ArchivedAt      int64  `json:"archived_at,omitempty"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
	// Roster comes with the single-team answers only: the leader first, then the members.
	Roster []TeamRoster `json:"roster,omitempty"`
}

// TeamRoster is one seat of a team with its role. The seat row (name, state, waiting, stage …) is
// the one GET /v1/seats returns, so it is the Seat type rather than a copy.
type TeamRoster struct {
	Role string `json:"role"`
	Seat
}

// TeamCreate is the POST /v1/teams body. A nil limit takes the server default.
type TeamCreate struct {
	ProjectKey string   `json:"project_key"`
	Name       string   `json:"name"`
	Leader     string   `json:"leader"`
	Members    []string `json:"members,omitempty"`
	Brief      string   `json:"brief,omitempty"`
	// RelayMode is leader (default) or direct; MergeWindowSec 0 is allowed.
	RelayMode       string `json:"relay_mode,omitempty"`
	RelayRoundLimit *int   `json:"relay_round_limit,omitempty"`
	RelayDailyLimit *int   `json:"relay_daily_limit,omitempty"`
	MergeWindowSec  *int   `json:"merge_window_sec,omitempty"`
}

// TeamPatch is the PATCH /v1/teams/{id} body: nil leaves a field alone, Archived true archives.
type TeamPatch struct {
	Name            *string `json:"name,omitempty"`
	Leader          *string `json:"leader,omitempty"`
	Brief           *string `json:"brief,omitempty"`
	RelayMode       *string `json:"relay_mode,omitempty"`
	RelayRoundLimit *int    `json:"relay_round_limit,omitempty"`
	RelayDailyLimit *int    `json:"relay_daily_limit,omitempty"`
	MergeWindowSec  *int    `json:"merge_window_sec,omitempty"`
	Archived        *bool   `json:"archived,omitempty"`
}

// TeamListOpts filters ListTeams. All asks for everyone's teams (needs seat.manage.others).
type TeamListOpts struct {
	Project         string
	IncludeArchived bool
	All             bool
}

// TeamRoute is one routing decision (GET /v1/teams/{id}/routes).
type TeamRoute struct {
	ID        int64    `json:"id"`
	MailID    int64    `json:"mail_id,omitempty"`
	FromRef   string   `json:"from_ref,omitempty"`
	ToSeatIDs []string `json:"to_seat_ids"`
	Rule      string   `json:"rule"`
	Hop       int      `json:"hop"`
	RouteKey  string   `json:"route_key,omitempty"`
	Outcome   string   `json:"outcome"`
	DecidedAt int64    `json:"decided_at"`
}

// TeamEval is one leader evaluation (GET /v1/teams/{id}/evals).
type TeamEval struct {
	ID        int64  `json:"id"`
	LeaderRef string `json:"leader_ref,omitempty"`
	TurnRef   string `json:"turn_ref,omitempty"`
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason,omitempty"`
	At        int64  `json:"at"`
}

func teamPath(id string) string { return "/v1/teams/" + url.PathEscape(id) }

func (c *Client) teamBody(method, path string, in any, out any) error {
	if in == nil {
		return c.doJSON(method, path, nil, out)
	}
	b, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("encode team request: %w", err)
	}
	return c.doJSON(method, path, bytes.NewReader(b), out)
}

// ListTeams lists teams. The rows carry no roster.
func (c *Client) ListTeams(o TeamListOpts) ([]Team, error) {
	q := url.Values{}
	if o.Project != "" {
		q.Set("project", o.Project)
	}
	if o.IncludeArchived {
		q.Set("include_archived", "1")
	}
	if o.All {
		q.Set("all", "1")
	}
	path := "/v1/teams"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Teams []Team `json:"teams"`
	}
	if err := c.doJSON(http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Teams, nil
}

// CreateTeam adds a team. Leader and members are seat ids or seat names of the caller.
func (c *Client) CreateTeam(in TeamCreate) (Team, error) {
	var out Team
	err := c.teamBody(http.MethodPost, "/v1/teams", in, &out)
	return out, err
}

// GetTeam reads one team with its roster.
func (c *Client) GetTeam(id string) (Team, error) {
	var out Team
	err := c.doJSON(http.MethodGet, teamPath(id), nil, &out)
	return out, err
}

// PatchTeam edits a team (a new leader is a seat id or name of the team's project).
func (c *Client) PatchTeam(id string, p TeamPatch) (Team, error) {
	var out Team
	err := c.teamBody(http.MethodPatch, teamPath(id), p, &out)
	return out, err
}

// ArchiveTeam archives a team: its rows stay, its name and seats are freed.
func (c *Client) ArchiveTeam(id string) (Team, error) {
	var out Team
	err := c.doJSON(http.MethodDelete, teamPath(id), nil, &out)
	return out, err
}

// AddTeamMember adds a member seat (id or name); adding one twice changes nothing.
func (c *Client) AddTeamMember(id, seat string) (Team, error) {
	var out Team
	err := c.teamBody(http.MethodPost, teamPath(id)+"/members", map[string]any{"seat": seat}, &out)
	return out, err
}

// RemoveTeamMember drops a member seat (id or name); the seat itself stays.
func (c *Client) RemoveTeamMember(id, seat string) (Team, error) {
	var out Team
	err := c.doJSON(http.MethodDelete, teamPath(id)+"/members/"+url.PathEscape(seat), nil, &out)
	return out, err
}

// teamRecordList reads one of the record lists (?limit=, newest first).
func teamRecordList[T any](c *Client, path, field string, limit int) ([]T, error) {
	if limit > 0 {
		q := url.Values{"limit": []string{strconv.Itoa(limit)}}
		path += "?" + q.Encode()
	}
	var out map[string][]T
	if err := c.doJSON(http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out[field], nil
}

// TeamRoutes lists a team's routing decisions, newest first (limit <= 0: the server default).
func (c *Client) TeamRoutes(id string, limit int) ([]TeamRoute, error) {
	return teamRecordList[TeamRoute](c, teamPath(id)+"/routes", "routes", limit)
}

// TeamEvals lists a team's leader evaluations, newest first.
func (c *Client) TeamEvals(id string, limit int) ([]TeamEval, error) {
	return teamRecordList[TeamEval](c, teamPath(id)+"/evals", "evals", limit)
}
