package mcpserver

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

// W2b: the steward's MCP surface. A steward session's gofer MCP is started by the server
// with GOFER_STEWARD=1 (see job.stewardMCPServer) and registers ONLY the tools below — the
// same "identity comes from the environment the server set" rule as the leader's. The tool
// list is only the honest half of the boundary: the steward credential the process runs
// with is default-deny on the server, so even a tool that was left out here (or a raw HTTP
// call) cannot do what the whitelist forbids.

// envSteward is set (to "1") by the job service for the steward's session job only.
const envSteward = "GOFER_STEWARD"

func stewardMode() bool { return strings.TrimSpace(os.Getenv(envSteward)) != "" }

// stewardUpdateInput is gofer_work_update as the steward sees it: the ordinary fields plus a
// non-final status.
type stewardUpdateInput struct {
	ID          string  `json:"id"`
	Rev         int64   `json:"rev,omitempty"`
	Title       *string `json:"title,omitempty"`
	Goal        *string `json:"goal,omitempty"`
	Status      *string `json:"status,omitempty"`
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

type workRemindInput struct {
	ID string `json:"id"`
	// At is the reminder time as unix seconds; Clear removes the reminder. Exactly one.
	At    int64 `json:"at,omitempty"`
	Clear bool  `json:"clear,omitempty"`
	// After is a convenience: seconds from now (instead of At).
	AfterSec int64 `json:"after_sec,omitempty"`
}

type workRemindOutput struct {
	ID       string `json:"id"`
	RemindAt int64  `json:"remind_at"`
	Cleared  bool   `json:"cleared"`
}

type workMergeSuggestInput struct {
	// TargetID is the item that stays; SourceID is the one that would be merged into it.
	TargetID string `json:"target_id"`
	SourceID string `json:"source_id"`
	Reason   string `json:"reason,omitempty"`
}

type workMergeSuggestOutput struct {
	Suggestion jobstore.WorkMergeSuggestion `json:"suggestion"`
	Recorded   bool                         `json:"recorded"`
	Note       string                       `json:"note"`
}

type sessionTailInput struct {
	ID string `json:"id"`
	// Bytes bounds how much of the transcript tail is read (default 64KB, max 256KB).
	Bytes int64 `json:"bytes,omitempty"`
}

type listJobsInput struct {
	Project string `json:"project,omitempty"`
	Status  string `json:"status,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type listJobsOutput struct {
	Jobs []jobView `json:"jobs"`
}

type stewardNotesInput struct {
	// Action is get (default), history, set or review_summary.
	Action string `json:"action,omitempty"`
	// Text is the new notes body (set) or the review's point of view (review_summary).
	Text string `json:"text,omitempty"`
	// Version is the version to read (get; 0 = latest) or, for set, the version you read
	// and are replacing (0 only for the very first write).
	Version int `json:"version,omitempty"`
}

type stewardNoteVersion struct {
	Version int    `json:"version"`
	By      string `json:"by,omitempty"`
	At      int64  `json:"at,omitempty"`
	Bytes   int    `json:"bytes"`
}

type stewardNotesOutput struct {
	Version  int                  `json:"version"`
	Body     string               `json:"body,omitempty"`
	By       string               `json:"by,omitempty"`
	At       int64                `json:"at,omitempty"`
	Bytes    int                  `json:"bytes"`
	NeedSlim bool                 `json:"need_slim"`
	History  []stewardNoteVersion `json:"history,omitempty"`
	Status   string               `json:"status,omitempty"`
}

// registerStewardTools builds the surface the steward's gofer MCP exposes. Anything absent
// here is absent on purpose: no job submit, no config, no review, no cancel, no delete, no
// merge (only a suggestion), no way to end a work item.
func registerStewardTools(s *mcp.Server, b Backend) {
	// Reads.
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_work_list",
		Description: "List the human's work items with the header counts. Filters: status[] (active|needs_me|waiting_resource|needs_onsite|review|parked|done|dropped), project, workspace, unsorted (drafts), query, due, include_closed. Each item carries goal / blocker / next step, its current sessions and in-flight requests.",
	}, workListHandler(b, ""))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_work_get",
		Description: "Read one work item with its journal (newest entries, oldest first), all sessions (current and past), links and field sources.",
	}, workGetHandler(b, ""))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_work_requests",
		Description: "Read the work request ledger (read-only): the report / hand-over / tidy-up asks and what became of them (pending|sent|answered|failed|expired). id limits it to one work item, active only the in-flight ones.",
	}, workRequestsHandler(b, ""))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_session_list",
		Description: "List registered terminal agent sessions (read-only): id, agent, project, runner, cwd, title, state, last message, last seen.",
	}, sessionListHandler(b, ""))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_session_get",
		Description: "Read one terminal agent session (read-only; id may be a unique prefix).",
	}, sessionGetHandler(b, ""))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_session_tail",
		Description: "Read the tail of a session's transcript as plain text (read-only, size-limited; id is the full session id). Falls back to the session's last message / progress line when the transcript cannot be read.",
	}, sessionTailHandler(b))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_list_jobs",
		Description: "List jobs (read-only): optional project / status filters, newest first. Only states — no logs, no prompts.",
	}, listJobsHandler(b))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_get_job",
		Description: "Get the current state of a job by id (read-only).",
	}, getJobHandler(b))

	registerIssueReadTools(s, b, "")

	// Writes: the descriptive / scheduling side of work items, nothing that decides.
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_work_update",
		Description: "Change a work item's descriptive fields (title, goal, blocker_kind/blocker_text, next_step, summary, project_key, workspace, priority, park_until / remind_at as unix seconds, park_note, unsorted). status may be a NON-final one (active|needs_me|waiting_resource|needs_onsite|review|parked); a status a person set wins and is reported back in `notes`, and done / dropped are refused — ending a work item is the person's call. Pass rev for an optimistic-lock check.",
	}, stewardWorkUpdateHandler(b))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_work_note",
		Description: "Append a note to a work item's journal (append-only; recorded as steward(<agent>)).",
	}, workNoteHandler(b, ""))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_work_remind",
		Description: "Set (at = unix seconds, or after_sec = seconds from now) or clear (clear=true) a work item's reminder. A due reminder notifies the person.",
	}, workRemindHandler(b))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_work_merge_suggest",
		Description: "Suggest that work item source_id is the same thing as target_id and should be merged into it. This ONLY records the suggestion (and a journal line on both items): a person confirms or dismisses it; nothing is merged.",
	}, workMergeSuggestHandler(b))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_work_request_report",
		Description: "Ask a work item's running session(s) to report (kind report, default) or to write a hand-over (kind handoff) through the request ledger; the session answers with gofer_work_report. Asynchronous: do not wait — read the result later with gofer_work_requests. A session that is not running is tidied up instead.",
	}, workRequestReportHandler(b, ""))
	registerSessionAskTool(s, b)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_work_summarize",
		Description: "Tidy a work item up now: a cheap one-shot read-only model reads the session transcript tail and fills goal / blocker / next. Fields a person or the session wrote become suggestions for the person to adopt (never overwritten). Returns the ledger request that tracks it.",
	}, workSummarizeHandler(b, ""))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_steward_notes",
		Description: "Your long-term notes (versioned Markdown). action=get (default; version=0 is the latest) | history (versions, no bodies) | set (text = the whole new notes, version = the version you read; a conflict means someone wrote since — get again and merge) | review_summary (text = your <=300-character point of view for today's digest, during a review). Keep the notes under 8KB; every set keeps the old version.",
	}, stewardNotesHandler(b))
}

func stewardWorkUpdateHandler(b Backend) mcp.ToolHandlerFor[stewardUpdateInput, work.DetailView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in stewardUpdateInput) (*mcp.CallToolResult, work.DetailView, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, work.DetailView{}, fmt.Errorf("id is required")
		}
		if in.Status != nil && jobstore.WorkStatusFinal(strings.TrimSpace(*in.Status)) {
			return nil, work.DetailView{}, fmt.Errorf("status %q is not allowed for the steward: done / dropped are the person's call", *in.Status)
		}
		p := jobstore.WorkItemPatch{Title: in.Title, Goal: in.Goal, Status: in.Status, BlockerKind: in.BlockerKind, BlockerText: in.BlockerText,
			NextStep: in.NextStep, Summary: in.Summary, ProjectKey: in.ProjectKey, Workspace: in.Workspace, Priority: in.Priority,
			ParkUntil: in.ParkUntil, ParkNote: in.ParkNote, RemindAt: in.RemindAt, Unsorted: in.Unsorted}
		d, err := b.UpdateWorkItem(in.ID, p, in.Rev)
		return nil, d, err
	}
}

func workRemindHandler(b Backend) mcp.ToolHandlerFor[workRemindInput, workRemindOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in workRemindInput) (*mcp.CallToolResult, workRemindOutput, error) {
		id := strings.TrimSpace(in.ID)
		if id == "" {
			return nil, workRemindOutput{}, fmt.Errorf("id is required")
		}
		var at int64
		switch {
		case in.Clear && (in.At != 0 || in.AfterSec != 0):
			return nil, workRemindOutput{}, fmt.Errorf("clear cannot be combined with at / after_sec")
		case in.Clear:
			at = 0
		case in.At > 0 && in.AfterSec > 0:
			return nil, workRemindOutput{}, fmt.Errorf("give at OR after_sec, not both")
		case in.At > 0:
			at = in.At
		case in.AfterSec > 0:
			at = time.Now().Unix() + in.AfterSec
		default:
			return nil, workRemindOutput{}, fmt.Errorf("give at (unix seconds), after_sec, or clear=true")
		}
		d, err := b.UpdateWorkItem(id, jobstore.WorkItemPatch{RemindAt: &at}, 0)
		if err != nil {
			return nil, workRemindOutput{}, err
		}
		return nil, workRemindOutput{ID: id, RemindAt: d.RemindAt, Cleared: at == 0}, nil
	}
}

func workMergeSuggestHandler(b Backend) mcp.ToolHandlerFor[workMergeSuggestInput, workMergeSuggestOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in workMergeSuggestInput) (*mcp.CallToolResult, workMergeSuggestOutput, error) {
		if strings.TrimSpace(in.TargetID) == "" || strings.TrimSpace(in.SourceID) == "" {
			return nil, workMergeSuggestOutput{}, fmt.Errorf("target_id and source_id are required")
		}
		sg, recorded, err := b.SuggestWorkMerge(strings.TrimSpace(in.TargetID), strings.TrimSpace(in.SourceID), in.Reason)
		if err != nil {
			return nil, workMergeSuggestOutput{}, err
		}
		note := "recorded: a person must confirm it; nothing was merged"
		if !recorded {
			note = "the same pair was already suggested and is still waiting for a person"
		}
		return nil, workMergeSuggestOutput{Suggestion: sg, Recorded: recorded, Note: note}, nil
	}
}

func sessionTailHandler(b Backend) mcp.ToolHandlerFor[sessionTailInput, work.SessionTailResult] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in sessionTailInput) (*mcp.CallToolResult, work.SessionTailResult, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, work.SessionTailResult{}, fmt.Errorf("id is required")
		}
		r, err := b.SessionTail(strings.TrimSpace(in.ID), in.Bytes)
		return nil, r, err
	}
}

func listJobsHandler(b Backend) mcp.ToolHandlerFor[listJobsInput, listJobsOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in listJobsInput) (*mcp.CallToolResult, listJobsOutput, error) {
		limit := in.Limit
		if limit <= 0 || limit > 100 {
			limit = 30
		}
		rows, err := b.ListJobViews(strings.TrimSpace(in.Project), strings.TrimSpace(in.Status), limit)
		if err != nil {
			return nil, listJobsOutput{}, err
		}
		out := make([]jobView, 0, len(rows))
		for _, r := range rows {
			out = append(out, toJobView(r))
		}
		return nil, listJobsOutput{Jobs: out}, nil
	}
}

func stewardNotesHandler(b Backend) mcp.ToolHandlerFor[stewardNotesInput, stewardNotesOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in stewardNotesInput) (*mcp.CallToolResult, stewardNotesOutput, error) {
		switch strings.ToLower(strings.TrimSpace(in.Action)) {
		case "", "get":
			out, err := b.StewardNotesGet(in.Version)
			return nil, out, err
		case "history":
			out, err := b.StewardNotesHistory()
			return nil, out, err
		case "set":
			if in.Text == "" {
				return nil, stewardNotesOutput{}, fmt.Errorf("text is required for set (it replaces the whole notes)")
			}
			out, err := b.StewardNotesSet(in.Text, in.Version)
			return nil, out, err
		case "review_summary":
			if strings.TrimSpace(in.Text) == "" {
				return nil, stewardNotesOutput{}, fmt.Errorf("text is required for review_summary")
			}
			if err := b.StewardReviewSummary(in.Text); err != nil {
				return nil, stewardNotesOutput{}, err
			}
			return nil, stewardNotesOutput{Status: "review summary recorded"}, nil
		default:
			return nil, stewardNotesOutput{}, fmt.Errorf("action must be get, history, set or review_summary")
		}
	}
}
