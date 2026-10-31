package work

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

// The capture box's dispatch (design 2026-10-16 §3): a captured item is handed to a
// session, linked to an already-submitted job, turned into a todo, archived or dropped.
// Only a captured item can be dispatched; the permission rules live in the handler.

// ErrNotCaptured is returned when the item is not in the capture box (any dispatch of an
// item that already left it is refused, so a double click cannot act twice).
var ErrNotCaptured = errors.New("work item is not in the capture box")

// Dispatch targets accepted by the HTTP route.
const (
	DispatchSession = "session"
	DispatchJob     = "job"
	DispatchTodo    = "todo"
	DispatchArchive = "archive"
	DispatchDrop    = "drop"
	DispatchIssue   = "issue"
)

// CapturedItem is a captured item as the dispatch sees it: the text to hand over and the
// revision the follow-up write is checked against.
type CapturedItem struct {
	Item jobstore.WorkItem
	Text string
}

// Captured loads item id and refuses (ErrNotCaptured) one that is not captured.
func (s *Service) Captured(id string) (CapturedItem, error) {
	w, ok, err := s.store.GetWorkItem(strings.TrimSpace(id))
	if err != nil {
		return CapturedItem{}, err
	}
	if !ok {
		return CapturedItem{}, jobstore.ErrWorkItemNotFound
	}
	if w.Status != jobstore.WorkCaptured {
		return CapturedItem{}, fmt.Errorf("%w: %s is %s", ErrNotCaptured, w.ID, w.Status)
	}
	text := strings.TrimSpace(w.CaptureText)
	if text == "" {
		text = strings.TrimSpace(w.Title)
	}
	if g := strings.TrimSpace(w.Goal); g != "" && g != text {
		text += "\n\n" + g
	}
	return CapturedItem{Item: w, Text: text}, nil
}

// FinishDispatch records a successful hand-over: an optional link (job / todo / plan
// kinds), a journal line, and the move out of captured to the given status. The item is
// read again first: one that left the capture box meanwhile (a second click) is
// ErrNotCaptured instead of a second dispatch. (The link write bumps the revision, so the
// revision read by Captured cannot be the check here.)
func (s *Service) FinishDispatch(id string, status, linkKind, linkRef, journal, by string) (jobstore.WorkItem, error) {
	if _, err := s.Captured(id); err != nil {
		return jobstore.WorkItem{}, err
	}
	if linkKind != "" {
		if _, err := s.store.AddWorkLink(id, linkKind, linkRef, by); err != nil {
			return jobstore.WorkItem{}, err
		}
	}
	src := jobstore.WorkSourceHuman
	w, _, err := s.store.UpdateWorkItem(id, jobstore.WorkItemPatch{
		Status: &status, StatusSource: &src, FromCapture: true, Quiet: true,
	}, 0, by)
	if err != nil {
		return jobstore.WorkItem{}, err
	}
	if journal != "" {
		if _, err := s.store.AppendWorkJournal(id, jobstore.WorkJournalNote, journal, by); err != nil {
			return w, err
		}
	}
	return w, nil
}

// DispatchToTodo converts a captured item into a todo (the to-todo rules) and closes the
// capture item after handing it over.
func (s *Service) DispatchToTodo(id string, in ToTodoInput) (ToTodoResult, jobstore.WorkItem, error) {
	c, err := s.Captured(id)
	if err != nil {
		return ToTodoResult{}, jobstore.WorkItem{}, err
	}
	res, err := s.ToTodo(c.Item.ID, in)
	if err != nil {
		return ToTodoResult{}, jobstore.WorkItem{}, err
	}
	w, err := s.FinishDispatch(c.Item.ID, jobstore.WorkDone, "", "",
		i18n.T(s.lang(c.Item.ProjectKey), "work.request.todo", "id", res.TodoID), in.By)
	return res, w, err
}

// DispatchClose archives (status done, journal 「归档」) or drops (status dropped) a captured
// item; to is DispatchArchive or DispatchDrop.
func (s *Service) DispatchClose(id, to, by string) (jobstore.WorkItem, error) {
	c, err := s.Captured(id)
	if err != nil {
		return jobstore.WorkItem{}, err
	}
	switch to {
	case DispatchArchive:
		return s.FinishDispatch(c.Item.ID, jobstore.WorkDone, "", "", i18n.T(s.lang(c.Item.ProjectKey), "work.journal.dispatch_archive"), by)
	case DispatchDrop:
		return s.FinishDispatch(c.Item.ID, jobstore.WorkDropped, "", "", i18n.T(s.lang(c.Item.ProjectKey), "work.journal.dispatch_drop"), by)
	}
	return jobstore.WorkItem{}, fmt.Errorf("%w: unknown close target %q", jobstore.ErrWorkInvalid, to)
}

// IssueTypeForKind is the tracker issue type a captured kind becomes: a captured issue is
// a bug, everything else (idea / request / note) a task.
func IssueTypeForKind(kind string) string {
	if kind == jobstore.WorkKindIssue {
		return "bug"
	}
	return "task"
}

// DispatchToIssue files the captured item as a new issue in the server mirror of
// trackerID (title: titleOverride or the item's title; description: the captured text plus
// where it came from, see issueDescription), links it (kind issue) and closes the item. The
// issue is flagged server-created until a repository sync pulls it (the web shows 「待同步」);
// the journal says so. The store's errors (ErrTrackerNoPrefix, ErrTrackerRepoUnknown,
// ErrReservedTracker) pass through unchanged. Returns the created record.
func (s *Service) DispatchToIssue(id, trackerID, titleOverride, by string) (jobstore.TrackerRecord, jobstore.WorkItem, error) {
	c, err := s.Captured(id)
	if err != nil {
		return jobstore.TrackerRecord{}, jobstore.WorkItem{}, err
	}
	title := strings.TrimSpace(titleOverride)
	if title == "" {
		title = c.Item.Title
	}
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		return jobstore.TrackerRecord{}, jobstore.WorkItem{}, fmt.Errorf("%w: title required", jobstore.ErrWorkInvalid)
	}
	desc := s.issueDescription(c.Item, title)
	rec, err := s.store.CreateServerTrackerIssue(strings.TrimSpace(trackerID), tracker.Issue{
		Title: title, Description: desc, Type: IssueTypeForKind(c.Item.Kind), Priority: 2,
	}, by)
	if err != nil {
		return jobstore.TrackerRecord{}, jobstore.WorkItem{}, err
	}
	w, err := s.FinishDispatch(c.Item.ID, jobstore.WorkDone, jobstore.WorkLinkIssue, rec.ID,
		i18n.T(s.lang(c.Item.ProjectKey), "work.request.issue", "id", rec.ID), by)
	return rec, w, err
}

// issueDescription is the description of the issue a captured item becomes. The captured text
// is all a person wrote, and a title only holds its head: rewriting the title on the way to an
// issue used to leave the rest of the text nowhere (gofer-wcga). So the whole captured text
// goes in unless the issue title IS that text, the goal is added when it says something the
// title does not, the attachment references found in the text are listed (the capture box keeps
// no attachment rows, a pasted image lives only as a path inside the text), and where the item
// came from (who, when, which session / job, project) is recorded. The last line is always the
// captured-item marker.
func (s *Service) issueDescription(w jobstore.WorkItem, title string) string {
	orig, goal := strings.TrimSpace(w.CaptureText), strings.TrimSpace(w.Goal)
	if orig == "" {
		orig = strings.TrimSpace(w.Title)
	}
	parts := []string{}
	if orig != "" && orig != title {
		parts = append(parts, orig)
	}
	if goal != "" && goal != title && goal != orig {
		parts = append(parts, goal)
	}
	parts = append(parts, s.captureSourceLine(w))
	desc := strings.Join(parts, "\n\n")
	if refs := attachmentRefs(orig, goal); len(refs) > 0 {
		desc += "\nattachments: " + strings.Join(refs, ", ")
	}
	return desc + "\n\ncaptured as work item " + w.ID
}

// captureSourceLine records where a captured item came from: who recorded it, when, the session
// or job it was recorded from (else the entry it went through), and its project.
func (s *Service) captureSourceLine(w jobstore.WorkItem) string {
	by := s.captureAuthor(w)
	line := fmt.Sprintf("captured by %s at %s from %s", by,
		time.Unix(w.CreatedAt, 0).UTC().Format(time.RFC3339), captureEntry(w, by))
	if p := strings.TrimSpace(w.ProjectKey); p != "" {
		line += ", project " + p
	}
	return line
}

// captureAuthor is who recorded the item: the author of its creation journal line, because
// updated_by follows whoever edited the item last (a title fixed while triaging is not the
// capture). The last writer is the fallback when the journal cannot be read.
func (s *Service) captureAuthor(w jobstore.WorkItem) string {
	if e, ok, err := s.store.FirstWorkJournal(w.ID); err == nil && ok {
		if by := strings.TrimSpace(e.By); by != "" {
			return by
		}
	}
	if by := strings.TrimSpace(w.UpdatedBy); by != "" {
		return by
	}
	return "unknown"
}

// captureEntry names the way an item entered the capture box: the session (and job) it was
// recorded from, else the mailbox (a letter nobody could be handed to is recorded there) or the
// capture box itself.
func captureEntry(w jobstore.WorkItem, by string) string {
	sid, jid := strings.TrimSpace(w.CaptureSourceSession), strings.TrimSpace(w.CaptureSourceJob)
	switch {
	case sid != "" && jid != "":
		return "session " + sid + " (job " + jid + ")"
	case sid != "":
		return "session " + sid
	case jid != "":
		return "job " + jid
	case by == "mail":
		return "the mailbox"
	}
	return "the capture box"
}

// attachmentRefPattern is an attachment path inside a captured item's text. The id shape is
// internal/attachment's own (16-40 of [A-Za-z0-9_-]), so a shorter fragment is not an id.
var attachmentRefPattern = regexp.MustCompile(`/v1/attachments/[A-Za-z0-9_-]{16,40}`)

// attachmentRefs lists the distinct attachment paths in the given texts, in order of appearance.
func attachmentRefs(texts ...string) []string {
	var out []string
	seen := make(map[string]struct{}, len(texts))
	for _, t := range texts {
		for _, ref := range attachmentRefPattern.FindAllString(t, -1) {
			if _, dup := seen[ref]; dup {
				continue
			}
			seen[ref] = struct{}{}
			out = append(out, ref)
		}
	}
	return out
}
