package httpapi

import (
	"errors"
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/mail"
)

// The `agent:` addresses' entry point (设计 2026-10-22 mail-agent-address「D6」): what names can
// be written, and which resident sessions serve them.

// mailResidentAgentView is one configured 收件 agent (mail.agents.<名字>).
type mailResidentAgentView struct {
	Name        string   `json:"name"`
	Agent       string   `json:"agent"`
	Runner      string   `json:"runner,omitempty"`
	IdleMinutes int      `json:"idle_minutes"`
	MaxLetters  int      `json:"max_letters"`
	MaxHours    int      `json:"max_hours"`
	Prompt      string   `json:"prompt,omitempty"`
	Args        []string `json:"args,omitempty"`
}

// mailResidentSessionView is one resident session that exists right now.
type mailResidentSessionView struct {
	Name      string `json:"name"`
	Project   string `json:"project,omitempty"`
	JobID     string `json:"job_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Letters   int    `json:"letters"`
	Pending   int    `json:"pending"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
	IdleSince int64  `json:"idle_since,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// mailAgentsView is the whole listing: what may be addressed, and what is running.
type mailAgentsView struct {
	MaxResident int                       `json:"agents_max_resident"`
	Agents      []mailResidentAgentView   `json:"agents"`
	Residents   []mailResidentSessionView `json:"residents"`
}

// GET /v1/mail/agents — the names an `agent:` address may use (mail.agents) and the resident
// sessions serving them.
func (s *Server) handleMailAgents(c *rux.Context) {
	if !s.mailReady(c) {
		return
	}
	cfg := s.mail.Residents()
	out := mailAgentsView{MaxResident: cfg.Max, Agents: []mailResidentAgentView{}, Residents: []mailResidentSessionView{}}
	for _, name := range cfg.Names() {
		spec, ok := cfg.Agent(name)
		if !ok {
			continue
		}
		out.Agents = append(out.Agents, mailResidentAgentView{
			Name: spec.Name, Agent: spec.Identity(), Runner: spec.Runner,
			IdleMinutes: int(spec.Idle.Minutes()), MaxLetters: spec.MaxLetters, MaxHours: spec.MaxHours,
			Prompt: spec.Prompt, Args: spec.Args,
		})
	}
	rows, err := s.mail.ListResidents()
	if err != nil {
		s.writeMailError(c, err)
		return
	}
	for _, r := range rows {
		out.Residents = append(out.Residents, mailResidentSessionView{
			Name: r.Name, Project: r.Project, JobID: r.JobID, SessionID: r.SessionID,
			Letters: r.Letters, Pending: r.Pending, Status: r.Status,
			CreatedAt: r.CreatedAt, IdleSince: r.IdleSince, LastError: r.LastError,
		})
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/mail/agents/{name}/end?project=<key> — end a resident session by hand. Ending one that
// is not running is 404 (there is nothing to end), 200 otherwise, with the job that was ended.
func (s *Server) handleMailAgentEnd(c *rux.Context) {
	if !s.mailReady(c) {
		return
	}
	name := c.Param("name")
	project := c.Query("project")
	jobID, err := s.mail.EndResident(name, project, "手动结束（web / CLI）")
	if err != nil {
		if errors.Is(err, mail.ErrAgentNotResident) {
			writeError(c, http.StatusNotFound, "no resident session", err.Error())
			return
		}
		s.writeMailError(c, err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"name": name, "project": project, "job_id": jobID, "status": mailAgentEndStatus})
}

// mailAgentEndStatus is the status a manually ended resident session is left in.
const mailAgentEndStatus = jobstore.MailResidentEnded
