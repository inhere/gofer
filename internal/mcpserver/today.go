package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/today"
)

// N3 T4: the steward's view of the 「等我」 list (GET /v1/today waiting, the one list the home page,
// the inbox and the badge share) and its one write there — advice. Advice never executes
// anything: the person clicks 「按建议」. The server re-checks that the caller is the steward (or a
// person) and validates the card / action.

type todayListInput struct {
	// Kind filters by card kind (approval|interaction|permission|decision|relay|review|plan_blocked|mail_held|job.failed).
	Kind string `json:"kind,omitempty"`
	// Waiting filters by 「等我」 category (approval|answer|review|plan_stuck|failed|delivery).
	Waiting string `json:"waiting,omitempty"`
	// Unadvised lists only the cards without advice yet (and that can take advice).
	Unadvised bool `json:"unadvised,omitempty"`
	// IncludeExec also lists needs_review jobs of the exec agent.
	IncludeExec bool `json:"include_exec,omitempty"`
}

type todayActionView struct {
	// ID is what advice action_id takes (answer actions are answer:<value>).
	ID    string `json:"id"`
	Label string `json:"label"`
	// Advisable is false for a reply box or a link: those cannot be 「按建议」.
	Advisable bool `json:"advisable"`
}

type todayCardView struct {
	Key          string `json:"key"`
	Kind         string `json:"kind"`
	Tag          string `json:"tag"`
	Urgency      string `json:"urgency"`
	Title        string `json:"title"`
	ProjectKey   string `json:"project_key,omitempty"`
	Agent        string `json:"agent,omitempty"`
	WaitingSince int64  `json:"waiting_since"`
	// Waiting / WaitingLabel are the 「等我」 category of the card and its label.
	Waiting      string            `json:"waiting,omitempty"`
	WaitingLabel string            `json:"waiting_label,omitempty"`
	Summary      string            `json:"summary"`
	Blocks       string            `json:"blocks,omitempty"`
	Review       *today.Review     `json:"review,omitempty"`
	Refs         today.Refs        `json:"refs"`
	Actions      []todayActionView `json:"actions"`
	Advice       *today.Advice     `json:"advice,omitempty"`
}

type todayListOutput struct {
	Cards []todayCardView `json:"cards"`
	Total int             `json:"total"`
}

type todayCardInput struct {
	CardKey string `json:"card_key"`
}

// todayJobDetail is what a review card's digest is written from: the job's own report,
// the diff stat, the commits and the verify outcome (the raw diff stays on the job page).
type todayJobDetail struct {
	JobID            string            `json:"job_id"`
	Status           string            `json:"status"`
	Report           string            `json:"report,omitempty"`
	DiffStat         string            `json:"diff_stat,omitempty"`
	Commits          []job.Commit      `json:"commits,omitempty"`
	Verify           *job.VerifyResult `json:"verify,omitempty"`
	UncommittedCount int               `json:"uncommitted_count,omitempty"`
	Error            string            `json:"error,omitempty"`
}

type todayCardOutput struct {
	Card todayCardView   `json:"card"`
	Job  *todayJobDetail `json:"job,omitempty"`
}

// todayReportRunes caps the job report a card read returns.
const todayReportRunes = 4000

type todayAdviseOutput struct {
	CardKey string       `json:"card_key"`
	Advice  today.Advice `json:"advice"`
	Note    string       `json:"note"`
}

func registerTodayTools(s *mcp.Server, b Backend) {
	addGoferTool(s, &mcp.Tool{
		Name:        "gofer_today_list",
		Description: "List the person's " + text(b, "mcp.term.waiting") + " list (read-only) — what is waiting on a person, taken from structured facts only (approvals, questions, reviews, blocked plans, failed jobs the person submitted, held mail; work items, model suggestions and merge / memory suggestions are not in it): each card's key, kind, waiting category and label, title, one-line summary, actions (id = what gofer_today_advise action_id takes), refs (job_id / interaction_id / session_id / …) and current advice. Filters: kind, waiting (category), unadvised (only cards without advice that can take advice), include_exec.",
	}, todayListHandler(b))
	addGoferTool(s, &mcp.Tool{
		Name:        "gofer_today_card",
		Description: "Read one " + text(b, "mcp.term.waiting") + " card by key (read-only). A card tied to a job (review / interaction / plan_blocked) also returns the job's report, diff stat, commits and verify outcome — what a review digest is written from.",
	}, todayCardHandler(b))
	addGoferTool(s, &mcp.Tool{
		Name:        "gofer_today_advise",
		Description: "Advise on one " + text(b, "mcp.term.waiting") + " card: text (one line, <=60 characters; your reasoning), optional action_id (one of the card's advisable action ids — the person sees it as the leading " + text(b, "mcp.term.as_advised") + " button and decides; nothing is executed), optional digest (<=5 lines, for review cards: " + text(b, "mcp.term.digest_sections") + "). A decision card takes background only — no action_id. A failed-job card (kind job.failed) cannot take advice. Writing again replaces the card's advice.",
	}, todayAdviseHandler(b))
}

func todayListHandler(b Backend) mcp.ToolHandlerFor[todayListInput, todayListOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in todayListInput) (*mcp.CallToolResult, todayListOutput, error) {
		resp, err := b.TodayQueue(in.IncludeExec)
		if err != nil {
			return nil, todayListOutput{}, err
		}
		kind, waiting := strings.TrimSpace(in.Kind), strings.TrimSpace(in.Waiting)
		out := todayListOutput{Cards: make([]todayCardView, 0, len(resp.Waiting)), Total: len(resp.Waiting)}
		for _, c := range resp.Waiting {
			if (kind != "" && c.Kind != kind) || (waiting != "" && c.Waiting != waiting) ||
				(in.Unadvised && (c.Advice != nil || !today.CanAdvise(c.Kind))) {
				continue
			}
			out.Cards = append(out.Cards, toTodayCardView(c))
		}
		return nil, out, nil
	}
}

func todayCardHandler(b Backend) mcp.ToolHandlerFor[todayCardInput, todayCardOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in todayCardInput) (*mcp.CallToolResult, todayCardOutput, error) {
		key := strings.TrimSpace(in.CardKey)
		if key == "" {
			return nil, todayCardOutput{}, fmt.Errorf("card_key is required")
		}
		resp, err := b.TodayQueue(true)
		if err != nil {
			return nil, todayCardOutput{}, err
		}
		for _, c := range resp.Waiting {
			if c.Key != key {
				continue
			}
			out := todayCardOutput{Card: toTodayCardView(c)}
			if c.Refs.JobID != "" {
				r, err := b.GetJob(c.Refs.JobID)
				if err != nil {
					return nil, todayCardOutput{}, err
				}
				out.Job = &todayJobDetail{JobID: r.ID, Status: r.Status, Report: jobReport(r.ResultJSON), DiffStat: r.DiffSummary,
					Commits: r.Commits, Verify: r.Verify, UncommittedCount: r.UncommittedCount, Error: r.Error}
			}
			return nil, out, nil
		}
		return nil, todayCardOutput{}, fmt.Errorf("no card %s in the current waiting list (it may have been handled already)", key)
	}
}

// jobReport is the job's own report text out of its result.json (summary / report /
// message / result), else the raw JSON, capped.
func jobReport(resultJSON string) string {
	if strings.TrimSpace(resultJSON) == "" {
		return ""
	}
	text := resultJSON
	var m map[string]any
	if json.Unmarshal([]byte(resultJSON), &m) == nil {
		for _, k := range []string{"summary", "report", "message", "result"} {
			if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
				text = v
				break
			}
		}
	}
	if r := []rune(text); len(r) > todayReportRunes {
		text = string(r[:todayReportRunes]) + "…"
	}
	return text
}

func toTodayCardView(c today.Card) todayCardView {
	v := todayCardView{Key: c.Key, Kind: c.Kind, Tag: c.Tag, Urgency: c.Urgency, Title: c.Title, ProjectKey: c.ProjectKey,
		Agent: c.Agent, WaitingSince: c.WaitingSince, Waiting: c.Waiting, WaitingLabel: c.WaitingLabel, Summary: c.Summary, Blocks: c.Blocks.Text, Review: c.Review,
		Refs: c.Refs, Advice: c.Advice, Actions: make([]todayActionView, 0, len(c.Actions))}
	for _, a := range c.Actions {
		v.Actions = append(v.Actions, todayActionView{ID: today.ActionKey(a), Label: a.Label, Advisable: today.CanAdvise(c.Kind) && today.Advisable(c.Kind, a)})
	}
	return v
}

func todayAdviseHandler(b Backend) mcp.ToolHandlerFor[today.AdviceInput, todayAdviseOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in today.AdviceInput) (*mcp.CallToolResult, todayAdviseOutput, error) {
		if strings.TrimSpace(in.CardKey) == "" || strings.TrimSpace(in.Text) == "" {
			return nil, todayAdviseOutput{}, fmt.Errorf("card_key and text are required")
		}
		adv, err := b.TodayAdvise(in)
		if err != nil {
			return nil, todayAdviseOutput{}, err
		}
		return nil, todayAdviseOutput{CardKey: strings.TrimSpace(in.CardKey), Advice: adv,
			Note: "recorded as advice: the person decides; nothing was executed"}, nil
	}
}

func (b *clientBackend) TodayQueue(includeExec bool) (today.Response, error) {
	return b.cli.Today(includeExec)
}

func (b *clientBackend) TodayAdvise(in today.AdviceInput) (today.Advice, error) {
	return b.cli.TodayAdvise(in)
}

func (b *localBackend) TodayQueue(bool) (today.Response, error) {
	return today.Response{}, errNeedsServer
}

func (b *localBackend) TodayAdvise(today.AdviceInput) (today.Advice, error) {
	return today.Advice{}, errNeedsServer
}
