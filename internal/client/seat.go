package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Seat is one entry of the seat roster (GET /v1/seats): a long-lived working position, the
// session currently sitting in it and what is derived from that session.
type Seat struct {
	ID         string `json:"id"`
	CallerID   string `json:"caller_id,omitempty"`
	ProjectKey string `json:"project_key"`
	Name       string `json:"name"`
	RelCwd     string `json:"rel_cwd,omitempty"`
	Purpose    string `json:"purpose,omitempty"`
	Charter    string `json:"charter,omitempty"`
	CharterBy  string `json:"charter_by"`
	Kind       string `json:"kind"`
	Agent      string `json:"agent,omitempty"`
	CurrentRef string `json:"current_ref,omitempty"`
	ArchivedAt int64  `json:"archived_at,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
	State      string `json:"state"`
	Waiting    string `json:"waiting,omitempty"`
	Unread     bool   `json:"unread,omitempty"`
	ActivityAt int64  `json:"activity_at,omitempty"`
	ThreadID   string `json:"thread_id,omitempty"`
}

// SeatCreate is the POST /v1/seats body.
type SeatCreate struct {
	ProjectKey string `json:"project_key"`
	Name       string `json:"name"`
	RelCwd     string `json:"rel_cwd,omitempty"`
	Purpose    string `json:"purpose,omitempty"`
	Charter    string `json:"charter,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Agent      string `json:"agent,omitempty"`
}

// SeatPatch is the PATCH /v1/seats/{id} body: nil leaves a field unchanged. Archived true
// archives the seat.
type SeatPatch struct {
	Name     *string `json:"name,omitempty"`
	RelCwd   *string `json:"rel_cwd,omitempty"`
	Purpose  *string `json:"purpose,omitempty"`
	Charter  *string `json:"charter,omitempty"`
	Agent    *string `json:"agent,omitempty"`
	Archived *bool   `json:"archived,omitempty"`
}

// SeatListOpts filters ListSeats. All asks for everyone's seats (needs seat.manage.others;
// without it the list stays the caller's own).
type SeatListOpts struct {
	Project         string
	IncludeArchived bool
	All             bool
}

// SeatOpenPlan is how to get back into a seat (GET /v1/seats/{id}/open-plan).
type SeatOpenPlan struct {
	SeatID        string `json:"seat_id"`
	Kind          string `json:"kind"`
	Runner        string `json:"runner,omitempty"`
	Cwd           string `json:"cwd,omitempty"`
	Resumable     bool   `json:"resumable"`
	Alive         bool   `json:"alive,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Sh            string `json:"sh,omitempty"`
	PowerShell    string `json:"powershell,omitempty"`
	NewSh         string `json:"new_sh,omitempty"`
	NewPowerShell string `json:"new_powershell,omitempty"`
	ThreadID      string `json:"thread_id,omitempty"`
	NeedsRestart  bool   `json:"needs_restart,omitempty"`
	Restarted     bool   `json:"restarted,omitempty"`
	JobID         string `json:"job_id,omitempty"`
}

func seatPath(id string) string { return "/v1/seats/" + url.PathEscape(id) }

func (c *Client) seatBody(method, path string, in any, out any) error {
	var rd *bytes.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode seat request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	if rd == nil {
		return c.doJSON(method, path, nil, out)
	}
	return c.doJSON(method, path, rd, out)
}

// ListSeats lists seats.
func (c *Client) ListSeats(o SeatListOpts) ([]Seat, error) {
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
	path := "/v1/seats"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Seats []Seat `json:"seats"`
	}
	if err := c.doJSON(http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Seats, nil
}

// CreateSeat creates a vacant seat.
func (c *Client) CreateSeat(in SeatCreate) (Seat, error) {
	var out Seat
	err := c.seatBody(http.MethodPost, "/v1/seats", in, &out)
	return out, err
}

// GetSeat reads one seat.
func (c *Client) GetSeat(id string) (Seat, error) {
	var out Seat
	err := c.doJSON(http.MethodGet, seatPath(id), nil, &out)
	return out, err
}

// PatchSeat edits a seat (purpose / charter changes are recorded with this client's session
// header as the writer, see Seat.CharterBy).
func (c *Client) PatchSeat(id string, p SeatPatch) (Seat, error) {
	var out Seat
	err := c.seatBody(http.MethodPatch, seatPath(id), p, &out)
	return out, err
}

// DeleteSeat removes a seat and its history.
func (c *Client) DeleteSeat(id string) error {
	return c.doJSON(http.MethodDelete, seatPath(id), nil, nil)
}

// BindSeat makes ref (a terminal session id, or an ACP job id for an acp seat) the session
// sitting in the seat. replace takes over a seat that already has another session.
func (c *Client) BindSeat(id, ref string, replace bool) (Seat, error) {
	var out Seat
	err := c.seatBody(http.MethodPost, seatPath(id)+"/bind", map[string]any{"ref": ref, "replace": replace}, &out)
	return out, err
}

// UnbindSeat empties a seat.
func (c *Client) UnbindSeat(id string) (Seat, error) {
	var out Seat
	err := c.doJSON(http.MethodPost, seatPath(id)+"/unbind", nil, &out)
	return out, err
}

// SeatOpenPlan returns how to open a seat; restart also starts an ended ACP seat's session.
func (c *Client) SeatOpenPlan(id string, restart bool) (SeatOpenPlan, error) {
	path := seatPath(id) + "/open-plan"
	if restart {
		path += "?restart=1"
	}
	var out SeatOpenPlan
	err := c.doJSON(http.MethodGet, path, nil, &out)
	return out, err
}

// SeatCandidates lists the seats a terminal session could be bound to.
func (c *Client) SeatCandidates(sessionID string) ([]Seat, error) {
	var out struct {
		Seats []Seat `json:"seats"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/sessions/"+url.PathEscape(sessionID)+"/seat-candidates", nil, &out); err != nil {
		return nil, err
	}
	return out.Seats, nil
}
