package httpapi

import (
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/workbench"
)

// attachmentRefView is a sent attachment as the history shows it (web AttachmentRef): the
// thumbnail needs the id (GET /v1/attachments/{id}?thumb=1) and the size for layout; an
// expired one keeps only the placeholder flag.
type attachmentRefView struct {
	ID      string `json:"id"`
	Mime    string `json:"mime,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Expired bool   `json:"expired,omitempty"`
}

func attachmentRefViews(list []jobstore.Attachment, now int64) []attachmentRefView {
	if len(list) == 0 {
		return nil
	}
	out := make([]attachmentRefView, 0, len(list))
	for _, a := range list {
		v := attachmentRefView{ID: a.ID, Mime: a.Mime, Width: a.Width, Height: a.Height}
		if a.ExpiresAt > 0 && a.ExpiresAt <= now {
			v = attachmentRefView{ID: a.ID, Expired: true}
		}
		out = append(out, v)
	}
	return out
}

// threadAttachments lists what was sent to a workbench thread (nil while the attachment
// store is not wired or the lookup fails: history is best effort, never an error).
func (s *Server) threadAttachments(threadID string) []jobstore.Attachment {
	if s.attachments == nil || s.jobs == nil || s.jobs.Meta() == nil {
		return nil
	}
	list, err := s.jobs.Meta().ListAttachmentsByThread(threadID)
	if err != nil {
		return nil
	}
	return list
}

// promptAttachments returns, for the n-th prompt (1-based) of job jobID, the attachments it was
// sent with — the join the ACP history uses. nil when there are none.
func (s *Server) promptAttachments(jobID, sessionID string) func(n int) any {
	if s.attachments == nil {
		return nil
	}
	threadID := workbench.JobThreadID(jobID, sessionID)
	return func(n int) any {
		var matched []jobstore.Attachment
		for _, a := range s.threadAttachments(threadID) {
			if a.RefKind == workbench.RefKindPrompt && a.RefID == workbench.PromptRef(jobID, n) {
				matched = append(matched, a)
			}
		}
		if views := attachmentRefViews(matched, time.Now().Unix()); len(views) > 0 {
			return views
		}
		return nil
	}
}

// decisionAttachments groups the attachments sent as relay-turn answers by decision id.
func (s *Server) decisionAttachments(sessionID string) map[string][]attachmentRefView {
	list := s.threadAttachments("r:" + sessionID)
	if len(list) == 0 {
		return nil
	}
	byDecision := map[string][]jobstore.Attachment{}
	for _, a := range list {
		if a.RefKind == workbench.RefKindDecision {
			byDecision[a.RefID] = append(byDecision[a.RefID], a)
		}
	}
	now := time.Now().Unix()
	out := make(map[string][]attachmentRefView, len(byDecision))
	for id, l := range byDecision {
		out[id] = attachmentRefViews(l, now)
	}
	return out
}

// sessionMessageView is an outbox message plus the images it was sent with.
type sessionMessageView struct {
	jobstore.SessionMessage
	Attachments []attachmentRefView `json:"attachments,omitempty"`
}

// outboxViews decorates the outbox page with the attachments sent as those messages
// (workbench turns to a terminal session; ref_kind message).
func (s *Server) outboxViews(sessionID string, list []jobstore.SessionMessage) []sessionMessageView {
	byMsg, byDecision := map[string][]jobstore.Attachment{}, map[string][]jobstore.Attachment{}
	for _, a := range s.threadAttachments("r:" + sessionID) {
		switch a.RefKind {
		case workbench.RefKindMessage:
			byMsg[a.RefID] = append(byMsg[a.RefID], a)
		case workbench.RefKindDecision:
			byDecision[a.RefID] = append(byDecision[a.RefID], a)
		}
	}
	now := time.Now().Unix()
	out := make([]sessionMessageView, 0, len(list))
	for _, m := range list {
		joined := append([]jobstore.Attachment(nil), byMsg[m.ID]...)
		if m.DecisionID != "" {
			joined = append(joined, byDecision[m.DecisionID]...)
		}
		out = append(out, sessionMessageView{SessionMessage: m, Attachments: attachmentRefViews(joined, now)})
	}
	return out
}
