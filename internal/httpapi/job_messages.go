package httpapi

import (
	"sort"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/secret"
	"github.com/inhere/gofer/internal/streaming"
	"github.com/inhere/gofer/internal/util"
)

// gofer-chd9: the job detail page's 「消息」 section answers "what did I actually send
// to the agent, and what did it answer" without a trip to the workbench. Both halves
// come from data gofer already stores:
//
//   - every user message is a job's stored request prompt (its request_json), the
//     session's first one being the prompt the job page was opened for. The injected
//     rules frame is split off (job.SplitInjectedRules) and rides along in `injected`
//     so the UI can offer it behind a toggle instead of dropping it.
//   - the turns a continuous session took after that — `session say` messages and the
//     agent's replies — are read from the job's ACP structured record
//     (artifacts/acp.jsonl, the source the workbench conversation uses). A say message
//     goes straight into the resident ACP process's input channel and is never stored
//     as a job of its own, so that file is the only trace those turns leave.
const (
	// messagesTranscriptBytes is the per-job transcript window. The newest bytes win:
	// a long-running session's recent turns are what the page is opened for.
	messagesTranscriptBytes = 256 << 10
	// messagesTranscriptBudget bounds the whole list's transcript I/O. A session of
	// many jobs spends it newest-first and falls back to the stored prompt for the
	// jobs it could not afford to read.
	messagesTranscriptBudget = 512 << 10
	// messagesReplyRunes caps one agent reply excerpt: the list summarises the
	// conversation, the full text stays in the workbench / the log page.
	messagesReplyRunes = 1000
)

// Message roles in the detail page's 消息 list.
const (
	roleUser  = "user"
	roleAgent = "agent"
)

// jobMessageView is one item of the list, oldest first.
type jobMessageView struct {
	// Role is "user" (what was SENT to the agent) or "agent" (an excerpt of what it
	// answered). Only those two: the list is the conversation, not the job timeline.
	Role string `json:"role"`
	// JobID is the job the item belongs to, so the UI can link back to it.
	JobID string `json:"job_id,omitempty"`
	// At is the unix second the item is anchored to: its job's start for a user
	// message, its end (its start while it still runs) for a reply. The structured
	// record carries no per-turn timestamps, so turns of one job share its anchor.
	At int64 `json:"at,omitempty"`
	// Text is the message body: the caller's own words (rules frame removed) or the
	// agent's reply excerpt.
	Text string `json:"text"`
	// Truncated marks a shortened item — a prompt the runner itself cut to 8000 runes,
	// or a reply excerpt.
	Truncated bool `json:"truncated,omitempty"`
	// Injected is the rules section this message's prompt was stripped of. It is
	// carried ONLY on the session's first message: every turn of a session gets the
	// same frame, and repeating it would bury the conversation.
	Injected string `json:"injected,omitempty"`
	// Omitted marks that content between the previous item and this one was not read
	// (the byte window began mid-conversation).
	Omitted bool `json:"omitted,omitempty"`
	// Attachments are the images a user message was sent with (workbench attachments).
	Attachments []attachmentRefView `json:"attachments,omitempty"`
}

// jobPrompt is a job's stored user message, already secret-stripped.
type jobPrompt struct {
	text     string
	injected string
	ok       bool
}

// jobMessages assembles the 「消息」 list for res: the messages sent to the agent
// across res's session (res alone when it has none), each followed by the agent's
// reply excerpt when a structured record holds one.
//
// Best-effort by design: a failed session lookup or unreadable transcript degrades to
// what the stored requests can show, never to an error. The list explains a job; it
// must not be able to fail the job snapshot it rides on.
func (s *Server) jobMessages(res job.JobResult) []jobMessageView {
	chain := s.messageChain(res)
	windows := make(map[string]streaming.TranscriptWindow, len(chain))
	budget := messagesTranscriptBudget
	// Read newest-first so the budget buys the RECENT turns; an older job whose
	// transcript does not fit falls back to its stored prompt, which is the user's
	// message anyway.
	for i := len(chain) - 1; i >= 0 && budget > 0; i-- {
		win := streaming.ReadTranscript(chain[i], min(budget, messagesTranscriptBytes))
		windows[chain[i].ID] = win
		budget -= int(win.Bytes)
	}

	out := make([]jobMessageView, 0, util.CapMul(len(chain), 2))
	for i, j := range chain {
		views := jobMessageViews(j, windows[j.ID], s.storedPrompt(j), i == 0)
		// The n-th user message of a job is its n-th prompt: unless the byte window began
		// mid-conversation, where the ordinals are unknown.
		if !windows[j.ID].Omitted {
			if atts := s.promptAttachments(j.ID, j.SessionID); atts != nil {
				n := 0
				for k := range views {
					if views[k].Role != roleUser {
						continue
					}
					n++
					if v, ok := atts(n).([]attachmentRefView); ok {
						views[k].Attachments = v
					}
				}
			}
		}
		out = append(out, views...)
	}
	if out == nil {
		out = []jobMessageView{}
	}
	return out
}

// messageChain returns the session's jobs oldest-first, always including res. A job
// without a session id is a chain of one.
func (s *Server) messageChain(res job.JobResult) []job.JobResult {
	if res.SessionID == "" {
		return []job.JobResult{res}
	}
	list, err := s.jobs.ListJobs(job.ListOpts{Session: res.SessionID, Limit: includeSessionJobsLimit})
	if err != nil || len(list) == 0 {
		return []job.JobResult{res}
	}
	sort.SliceStable(list, func(i, k int) bool { return list[i].StartedAt < list[k].StartedAt })
	for _, j := range list {
		if j.ID == res.ID {
			return list
		}
	}
	// The list holds the newest includeSessionJobsLimit rows: an older job that was
	// asked for is prepended, so the page never hides its own message.
	return append([]job.JobResult{res}, list...)
}

// storedPrompt returns a job's user message as stored: the request prompt with the
// injected rules frame split off. It reads through RedactedRequest, so the prompt
// goes through the same secret stripping as GET /v1/jobs/{id}/request (and a job
// whose record was already redacted shows the placeholder).
//
// ok=false when the job has no readable stored request.
func (s *Server) storedPrompt(res job.JobResult) jobPrompt {
	req, ok, _, err := s.jobs.RedactedRequest(res.ID)
	if err != nil || !ok || req.Prompt == "" {
		return jobPrompt{}
	}
	text, injected := job.SplitInjectedRules(req.Prompt)
	text, _ = secret.RedactString(text)
	injected, _ = secret.RedactString(injected)
	return jobPrompt{text: text, injected: injected, ok: true}
}

// jobMessageViews turns one job's transcript window into its list items. p is the
// job's stored prompt; exposeInjected asks for the rules frame to ride along (the
// session's first message only).
func jobMessageViews(res job.JobResult, win streaming.TranscriptWindow, p jobPrompt, exposeInjected bool) []jobMessageView {
	var out []jobMessageView
	// gap marks the next item as following content the byte window skipped.
	gap := false
	switch {
	case win.Omitted && p.ok:
		// The window begins mid-conversation, so this job's own prompt — the message
		// the reader came for — is not inside it. Emit the stored copy first.
		out = append(out, userMessageView(res, p, exposeInjected))
		exposeInjected = false
		gap = true
	case len(win.Turns) == 0:
		// No structured record (an exec or plain-CLI agent job, or the record is
		// gone): the stored request is the only trace of what was sent.
		if p.ok {
			out = append(out, userMessageView(res, p, exposeInjected))
		}
		return out
	}

	for i, turn := range win.Turns {
		text, truncated := turn.User, turn.UserTruncated
		injected := ""
		switch {
		case i == 0 && !win.Omitted && p.ok:
			// The window starts at the job's first turn: the stored request is the
			// authoritative copy of it (complete, where the record cuts a long prompt
			// at 8000 runes) and the one that still holds the injected frame.
			text, truncated = p.text, false
			if exposeInjected {
				injected = p.injected
			}
		case text != "":
			// A later turn is a `session say` message (or a continuation prompt): it
			// normally carries no frame, but never show one if it does.
			text, _ = job.SplitInjectedRules(text)
			text, _ = secret.RedactString(text)
		}
		if text != "" {
			out = append(out, jobMessageView{Role: roleUser, JobID: res.ID, At: res.StartedAt,
				Text: text, Truncated: truncated, Injected: injected, Omitted: gap})
			gap = false
		}
		if turn.Reply != "" {
			reply, truncated := excerptRunes(turn.Reply, messagesReplyRunes)
			reply, _ = secret.RedactString(reply)
			out = append(out, jobMessageView{Role: roleAgent, JobID: res.ID, At: replyAnchor(res),
				Text: reply, Truncated: truncated, Omitted: gap})
			gap = false
		}
	}
	return out
}

func userMessageView(res job.JobResult, p jobPrompt, exposeInjected bool) jobMessageView {
	view := jobMessageView{Role: roleUser, JobID: res.ID, At: res.StartedAt, Text: p.text}
	if exposeInjected {
		view.Injected = p.injected
	}
	return view
}

// replyAnchor is when a reply happened as far as the job row knows: its end, or its
// start while it still runs (a live session's turn has no end yet).
func replyAnchor(res job.JobResult) int64 {
	if res.EndedAt != 0 {
		return res.EndedAt
	}
	return res.StartedAt
}

// excerptRunes cuts text to limit runes, reporting whether it cut anything.
func excerptRunes(text string, limit int) (string, bool) {
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return text, false
	}
	return string(runes[:limit]), true
}
