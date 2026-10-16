package work

import (
	"errors"
	"fmt"
	"strings"

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
	text := strings.TrimSpace(w.Title)
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

// DispatchToTodo converts a captured item into a todo (the to-todo rules) and moves it
// to active.
func (s *Service) DispatchToTodo(id string, in ToTodoInput) (ToTodoResult, jobstore.WorkItem, error) {
	c, err := s.Captured(id)
	if err != nil {
		return ToTodoResult{}, jobstore.WorkItem{}, err
	}
	res, err := s.ToTodo(c.Item.ID, in)
	if err != nil {
		return ToTodoResult{}, jobstore.WorkItem{}, err
	}
	w, err := s.FinishDispatch(c.Item.ID, jobstore.WorkActive, "", "",
		fmt.Sprintf("已转为 todo %s（plan %s）", res.TodoID, res.PlanID), in.By)
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
		return s.FinishDispatch(c.Item.ID, jobstore.WorkDone, "", "", "归档", by)
	case DispatchDrop:
		return s.FinishDispatch(c.Item.ID, jobstore.WorkDropped, "", "", "丢弃", by)
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
// trackerID (title: titleOverride or the item's title; description: its goal, plus where it
// came from), links it (kind issue) and parks the item. The issue is flagged server-created
// until a repository sync pulls it (the web shows 「待同步」); the journal says so. The
// store's errors (ErrTrackerNoPrefix, ErrTrackerRepoUnknown, ErrReservedTracker) pass
// through unchanged. Returns the created record.
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
	desc := strings.TrimSpace(c.Item.Goal)
	if desc == title {
		desc = ""
	}
	if desc != "" {
		desc += "\n\n"
	}
	desc += "captured as work item " + c.Item.ID
	rec, err := s.store.CreateServerTrackerIssue(strings.TrimSpace(trackerID), tracker.Issue{
		Title: title, Description: desc, Type: IssueTypeForKind(c.Item.Kind), Priority: 2,
	}, by)
	if err != nil {
		return jobstore.TrackerRecord{}, jobstore.WorkItem{}, err
	}
	w, err := s.FinishDispatch(c.Item.ID, jobstore.WorkParked, jobstore.WorkLinkIssue, rec.ID,
		fmt.Sprintf("已转为 issue %s（%s），待仓库同步", rec.ID, rec.TrackerID), by)
	return rec, w, err
}
