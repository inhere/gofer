package mcpserver

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

// W1 work items over MCP: gofer_work_list/get/update/note/report plus the read-only
// gofer_session_list/get. They are NOT on the leader whitelist (a leader only talks in
// its plan's threads and moves its checklist), and a project-scoped MCP only sees and
// touches its own project's items.

// sessionToolView is the read-only session projection both backends return.
type sessionToolView struct {
	SessionID    string `json:"session_id"`
	Agent        string `json:"agent"`
	ProjectKey   string `json:"project_key,omitempty"`
	Runner       string `json:"runner,omitempty"`
	Cwd          string `json:"cwd,omitempty"`
	Title        string `json:"title,omitempty"`
	State        string `json:"state"`
	RelayMode    string `json:"relay_mode,omitempty"`
	TurnNo       int64  `json:"turn_no"`
	LastMessage  string `json:"last_message,omitempty"`
	LastEvent    string `json:"last_event,omitempty"`
	LastSeenAt   int64  `json:"last_seen_at"`
	StartedAt    int64  `json:"started_at"`
	EndedAt      int64  `json:"ended_at,omitempty"`
	ProgressText string `json:"progress_text,omitempty"`
}

const maxSessionToolMessage = 4000

func capRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func sessionToolFromStore(a jobstore.AgentSession) sessionToolView {
	return sessionToolView{
		SessionID: a.SessionID, Agent: a.Agent, ProjectKey: a.ProjectKey, Runner: a.Runner, Cwd: a.Cwd, Title: a.Title,
		State: a.State, RelayMode: a.RelayMode, TurnNo: a.TurnNo, LastMessage: capRunes(a.LastMessage, maxSessionToolMessage),
		LastEvent: a.LastEvent, LastSeenAt: a.LastSeenAt, StartedAt: a.StartedAt, EndedAt: a.EndedAt, ProgressText: a.ProgressText,
	}
}

func sessionToolFromClient(a client.AgentSession) sessionToolView {
	return sessionToolView{
		SessionID: a.SessionID, Agent: a.Agent, ProjectKey: a.ProjectKey, Runner: a.Runner, Cwd: a.Cwd, Title: a.Title,
		State: a.State, RelayMode: a.RelayMode, TurnNo: a.TurnNo, LastMessage: capRunes(a.LastMessage, maxSessionToolMessage),
		LastEvent: a.LastEvent, LastSeenAt: a.LastSeenAt, StartedAt: a.StartedAt, EndedAt: a.EndedAt, ProgressText: a.ProgressText,
	}
}

type workListInput struct {
	Status        []string `json:"status,omitempty"`
	Project       string   `json:"project,omitempty"`
	Workspace     string   `json:"workspace,omitempty"`
	Unsorted      *bool    `json:"unsorted,omitempty"`
	Query         string   `json:"query,omitempty"`
	IncludeClosed bool     `json:"include_closed,omitempty"`
	Due           bool     `json:"due,omitempty"`
	SessionID     string   `json:"session_id,omitempty"`
	Limit         int      `json:"limit,omitempty"`
}

type workListOutput struct {
	Items   []work.ItemView `json:"items"`
	Summary work.Summary    `json:"summary"`
}

type workIDInput struct {
	ID string `json:"id"`
}

type workGetInput struct {
	ID           string `json:"id"`
	JournalLimit int    `json:"journal_limit,omitempty"`
}

type workUpdateInput struct {
	ID          string  `json:"id"`
	Rev         int64   `json:"rev,omitempty"`
	Title       *string `json:"title,omitempty"`
	Goal        *string `json:"goal,omitempty"`
	BlockerKind *string `json:"blocker_kind,omitempty"`
	BlockerText *string `json:"blocker_text,omitempty"`
	NextStep    *string `json:"next_step,omitempty"`
	Summary     *string `json:"summary,omitempty"`
	ProjectKey  *string `json:"project_key,omitempty"`
	Workspace   *string `json:"workspace,omitempty"`
	Priority    *int    `json:"priority,omitempty"`
	ParkUntil   *int64  `json:"park_until,omitempty"`
	ParkNote    *string `json:"park_note,omitempty"`
	RemindAt    *int64  `json:"remind_at,omitempty"`
	Unsorted    *bool   `json:"unsorted,omitempty"`
}

type workNoteInput struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type workReportInput struct {
	ID        string `json:"id"`
	Goal      string `json:"goal,omitempty"`
	Status    string `json:"status,omitempty"`
	Blocker   string `json:"blocker,omitempty"`
	Next      string `json:"next,omitempty"`
	Summary   string `json:"summary,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// Request is the request id this report answers (from the request text).
	Request string `json:"request,omitempty"`
}

type workRequestsInput struct {
	// ID limits the ledger to one work item ("" = every item).
	ID     string `json:"id,omitempty"`
	Active bool   `json:"active,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type workRequestsOutput struct {
	Requests []jobstore.WorkRequest `json:"requests"`
}

type workRequestReportInput struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id,omitempty"`
	// Kind is report (default) or handoff.
	Kind string `json:"kind,omitempty"`
}

type workRequestReportOutput struct {
	Results []work.RequestOutcome `json:"results"`
}

type workSummarizeOutput struct {
	Request jobstore.WorkRequest `json:"request"`
}

type sessionListInput struct {
	Project      string `json:"project,omitempty"`
	State        string `json:"state,omitempty"`
	Agent        string `json:"agent,omitempty"`
	IncludeEnded bool   `json:"include_ended,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

type sessionListOutput struct {
	Sessions []sessionToolView `json:"sessions"`
}

func registerWorkTools(s *mcp.Server, b Backend, scoped string) {
	registerIssueReadTools(s, b, scoped)
	if scoped == "" {
		registerSessionAskTool(s, b) // crosses sessions: not offered to a project-scoped MCP
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_work_list",
		Description: "List the human's work items (what they are doing, across terminal sessions) with the header counts. Filters: status[] (active|needs_me|waiting_resource|needs_onsite|review|parked|done|dropped), project, workspace, unsorted, query, due (reminder/park deadline passed), include_closed. Each item carries goal / blocker / next step, its current sessions and links.",
	}, workListHandler(b, scoped))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_work_get",
		Description: "Read one work item with its journal (newest entries, oldest first), all sessions (current and past) and links.",
	}, workGetHandler(b, scoped))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_work_update",
		Description: "Change a work item's descriptive fields (title, goal, blocker_kind/blocker_text, next_step, summary, project_key, workspace, priority, park_until / remind_at as unix seconds, park_note, unsorted). Pass rev to get an optimistic-lock check (conflict if the item changed). It does NOT set the status: a status is the human's call or goes through gofer_work_report, which respects the status a person set.",
	}, workUpdateHandler(b, scoped))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_work_note",
		Description: "Append a note to a work item's journal (append-only).",
	}, workNoteHandler(b, scoped))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_work_report",
		Description: "Report where a work item stands (goal / status / blocker / next / summary; session_id names the reporting session; request is the request id from a report / hand-over request you were asked to answer — it marks that request answered). Use it when asked to report. status active means 'the blocker is gone' and releases a status the human set; any other status is ignored while the human's own status stands, and done/dropped are never taken from a report.",
	}, workReportHandler(b, scoped))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_work_requests",
		Description: "Read the work request ledger (read-only): the report / hand-over / tidy-up asks and what became of them (pending|sent|answered|failed|expired). id limits it to one work item, active only the in-flight ones.",
	}, workRequestsHandler(b, scoped))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_work_request_report",
		Description: "Ask a work item's running session(s) to report (kind report, default) or to write a hand-over (kind handoff) through the request ledger; the session answers with gofer_work_report / `gofer work report --request <id>`. A session that is not running is tidied up instead. Needs a running gofer server.",
	}, workRequestReportHandler(b, scoped))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_work_summarize",
		Description: "Tidy a work item up now: a cheap one-shot read-only model reads the session transcript tail and fills goal / blocker / next. Fields a person or the session wrote become suggestions for the person to adopt (never overwritten); the status is only a hint. Returns the ledger request that tracks it. Needs a running gofer server.",
	}, workSummarizeHandler(b, scoped))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_session_list",
		Description: "List registered terminal agent sessions (read-only): id, agent, project, runner, cwd, title, state, last message, last seen.",
	}, sessionListHandler(b, scoped))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_session_get",
		Description: "Read one terminal agent session (read-only; id may be a unique prefix).",
	}, sessionGetHandler(b, scoped))
}

// workScopeCheck enforces the project scope on a by-id access: a project-scoped MCP only
// touches items of its own project (a mis-touch guard, like the other tools).
func workScopeCheck(b Backend, scoped, id string) error {
	if scoped == "" {
		return nil
	}
	d, err := b.GetWorkItem(id)
	if err != nil {
		return err
	}
	if d.ProjectKey != scoped {
		return fmt.Errorf("project-scoped MCP(--project %s): work item %s belongs to project %q", scoped, id, d.ProjectKey)
	}
	return nil
}

func workListHandler(b Backend, scoped string) mcp.ToolHandlerFor[workListInput, workListOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in workListInput) (*mcp.CallToolResult, workListOutput, error) {
		o := jobstore.WorkListOpts{Statuses: in.Status, Project: strings.TrimSpace(in.Project), Workspace: in.Workspace,
			Unsorted: in.Unsorted, Query: in.Query, IncludeClosed: in.IncludeClosed, Due: in.Due, SessionID: in.SessionID, Limit: in.Limit}
		if scoped != "" {
			if o.Project != "" && o.Project != scoped {
				return nil, workListOutput{}, fmt.Errorf("project-scoped MCP(--project %s): cannot list project %q", scoped, o.Project)
			}
			o.Project = scoped
		}
		items, sum, err := b.ListWorkItems(o)
		if err != nil {
			return nil, workListOutput{}, err
		}
		if items == nil {
			items = []work.ItemView{}
		}
		return nil, workListOutput{Items: items, Summary: sum}, nil
	}
}

func workGetHandler(b Backend, scoped string) mcp.ToolHandlerFor[workGetInput, work.DetailView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in workGetInput) (*mcp.CallToolResult, work.DetailView, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, work.DetailView{}, fmt.Errorf("id is required")
		}
		d, err := b.GetWorkItem(in.ID)
		if err != nil {
			return nil, work.DetailView{}, err
		}
		if scoped != "" && d.ProjectKey != scoped {
			return nil, work.DetailView{}, fmt.Errorf("project-scoped MCP(--project %s): work item %s belongs to project %q", scoped, in.ID, d.ProjectKey)
		}
		return nil, d, nil
	}
}

func workUpdateHandler(b Backend, scoped string) mcp.ToolHandlerFor[workUpdateInput, work.DetailView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in workUpdateInput) (*mcp.CallToolResult, work.DetailView, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, work.DetailView{}, fmt.Errorf("id is required")
		}
		if err := workScopeCheck(b, scoped, in.ID); err != nil {
			return nil, work.DetailView{}, err
		}
		if scoped != "" && in.ProjectKey != nil && *in.ProjectKey != scoped {
			return nil, work.DetailView{}, fmt.Errorf("project-scoped MCP(--project %s): cannot move an item to project %q", scoped, *in.ProjectKey)
		}
		p := jobstore.WorkItemPatch{Title: in.Title, Goal: in.Goal, BlockerKind: in.BlockerKind, BlockerText: in.BlockerText,
			NextStep: in.NextStep, Summary: in.Summary, ProjectKey: in.ProjectKey, Workspace: in.Workspace, Priority: in.Priority,
			ParkUntil: in.ParkUntil, ParkNote: in.ParkNote, RemindAt: in.RemindAt, Unsorted: in.Unsorted}
		d, err := b.UpdateWorkItem(in.ID, p, in.Rev)
		return nil, d, err
	}
}

func workNoteHandler(b Backend, scoped string) mcp.ToolHandlerFor[workNoteInput, map[string]string] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in workNoteInput) (*mcp.CallToolResult, map[string]string, error) {
		if strings.TrimSpace(in.ID) == "" || strings.TrimSpace(in.Text) == "" {
			return nil, nil, fmt.Errorf("id and text are required")
		}
		if err := workScopeCheck(b, scoped, in.ID); err != nil {
			return nil, nil, err
		}
		if err := b.AddWorkNote(in.ID, in.Text); err != nil {
			return nil, nil, err
		}
		return nil, map[string]string{"status": "noted", "id": in.ID}, nil
	}
}

func workReportHandler(b Backend, scoped string) mcp.ToolHandlerFor[workReportInput, work.DetailView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in workReportInput) (*mcp.CallToolResult, work.DetailView, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, work.DetailView{}, fmt.Errorf("id is required")
		}
		if err := workScopeCheck(b, scoped, in.ID); err != nil {
			return nil, work.DetailView{}, err
		}
		by := "mcp"
		if j := strings.TrimSpace(os.Getenv(envJobID)); j != "" {
			by = "job:" + j
		}
		d, err := b.ReportWork(in.ID, work.ReportInput{Goal: in.Goal, Status: in.Status, Blocker: in.Blocker, Next: in.Next, Summary: in.Summary, By: by, RequestID: in.Request}, in.SessionID)
		return nil, d, err
	}
}

func workRequestsHandler(b Backend, scoped string) mcp.ToolHandlerFor[workRequestsInput, workRequestsOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in workRequestsInput) (*mcp.CallToolResult, workRequestsOutput, error) {
		id := strings.TrimSpace(in.ID)
		if id == "" && scoped != "" {
			return nil, workRequestsOutput{}, fmt.Errorf("project-scoped MCP(--project %s): name a work item id", scoped)
		}
		if id != "" {
			if err := workScopeCheck(b, scoped, id); err != nil {
				return nil, workRequestsOutput{}, err
			}
		}
		reqs, err := b.ListWorkRequests(id, in.Active, in.Limit)
		if err != nil {
			return nil, workRequestsOutput{}, err
		}
		if reqs == nil {
			reqs = []jobstore.WorkRequest{}
		}
		return nil, workRequestsOutput{Requests: reqs}, nil
	}
}

func workRequestReportHandler(b Backend, scoped string) mcp.ToolHandlerFor[workRequestReportInput, workRequestReportOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in workRequestReportInput) (*mcp.CallToolResult, workRequestReportOutput, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, workRequestReportOutput{}, fmt.Errorf("id is required")
		}
		if err := workScopeCheck(b, scoped, in.ID); err != nil {
			return nil, workRequestReportOutput{}, err
		}
		out, err := b.RequestWorkReport(in.ID, strings.TrimSpace(in.SessionID), strings.TrimSpace(in.Kind))
		if out == nil {
			out = []work.RequestOutcome{}
		}
		return nil, workRequestReportOutput{Results: out}, err
	}
}

func workSummarizeHandler(b Backend, scoped string) mcp.ToolHandlerFor[workIDInput, workSummarizeOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in workIDInput) (*mcp.CallToolResult, workSummarizeOutput, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, workSummarizeOutput{}, fmt.Errorf("id is required")
		}
		if err := workScopeCheck(b, scoped, in.ID); err != nil {
			return nil, workSummarizeOutput{}, err
		}
		req, err := b.SummarizeWork(in.ID)
		return nil, workSummarizeOutput{Request: req}, err
	}
}

func sessionListHandler(b Backend, scoped string) mcp.ToolHandlerFor[sessionListInput, sessionListOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in sessionListInput) (*mcp.CallToolResult, sessionListOutput, error) {
		o := jobstore.ListSessionsOpts{Project: strings.TrimSpace(in.Project), State: in.State, Agent: in.Agent, IncludeEnded: in.IncludeEnded, Limit: in.Limit}
		if scoped != "" {
			if o.Project != "" && o.Project != scoped {
				return nil, sessionListOutput{}, fmt.Errorf("project-scoped MCP(--project %s): cannot list project %q", scoped, o.Project)
			}
			o.Project = scoped
		}
		list, err := b.ListSessionViews(o)
		if err != nil {
			return nil, sessionListOutput{}, err
		}
		if list == nil {
			list = []sessionToolView{}
		}
		return nil, sessionListOutput{Sessions: list}, nil
	}
}

func sessionGetHandler(b Backend, scoped string) mcp.ToolHandlerFor[workIDInput, sessionToolView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in workIDInput) (*mcp.CallToolResult, sessionToolView, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, sessionToolView{}, fmt.Errorf("id is required")
		}
		v, err := b.GetSessionView(in.ID)
		if err != nil {
			return nil, sessionToolView{}, err
		}
		if scoped != "" && v.ProjectKey != scoped {
			return nil, sessionToolView{}, fmt.Errorf("project-scoped MCP(--project %s): session %s belongs to project %q", scoped, in.ID, v.ProjectKey)
		}
		return nil, v, nil
	}
}
