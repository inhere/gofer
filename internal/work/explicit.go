package work

import (
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// Work items are explicit (design 2026-11-01 §4.2): a terminal session no longer gets one
// on its own. An item comes from a person (Web / CLI), from a plan binding, or from a
// session that says so with `gofer work report` — the functions below are the last two
// halves of that: the first report of a session creates the item, and the one-shot cleanup
// archives what the old per-session auto draft left behind.

// ErrSessionNeeded is returned by ReportBySession for a report that names no session.
var ErrSessionNeeded = fmt.Errorf("%w: a report without a work item id needs the reporting session (run it in the terminal session, or pass --session)", jobstore.ErrWorkInvalid)

// SessionReportInput is a session's self-report addressed by session instead of by item.
type SessionReportInput struct {
	ReportInput
	// Title names the item a first report creates. It is an explicit field of the report
	// (`--title`); empty falls back to the first line of Goal, never to a prompt.
	Title string
}

// ReportBySession is `gofer work report` without an item id. A session that is currently
// on an open work item reports on it (the newest one); a session that is not
// gets one created by this very report, titled by the report's own explicit title (else
// its goal) — never by the session's first message. It returns whether the item was
// created.
func (s *Service) ReportBySession(sid string, in SessionReportInput) (jobstore.WorkItem, bool, error) {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return jobstore.WorkItem{}, false, ErrSessionNeeded
	}
	if strings.TrimSpace(in.Goal) == "" && strings.TrimSpace(in.Status) == "" && strings.TrimSpace(in.Blocker) == "" &&
		strings.TrimSpace(in.Next) == "" && strings.TrimSpace(in.Summary) == "" {
		return jobstore.WorkItem{}, false, ErrEmptyReport
	}
	a, ok, err := s.store.GetAgentSession(sid)
	if err != nil {
		return jobstore.WorkItem{}, false, err
	}
	if !ok {
		return jobstore.WorkItem{}, false, ErrSessionNotFound
	}
	items, err := s.store.ListWorkItems(jobstore.WorkListOpts{SessionID: sid, Limit: 50})
	if err != nil {
		return jobstore.WorkItem{}, false, err
	}
	for _, it := range items { // newest activity first; only an item the session is still on
		rows, err := s.store.ListWorkItemSessions(it.ID)
		if err != nil {
			return jobstore.WorkItem{}, false, err
		}
		for _, r := range rows {
			if r.SessionID == sid && r.Role == jobstore.WorkSessionCurrent {
				w, err := s.Report(it.ID, in.ReportInput)
				return w, false, err
			}
		}
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = jobstore.FirstLine(strings.TrimSpace(in.Goal), 120)
	}
	if title == "" {
		return jobstore.WorkItem{}, false, fmt.Errorf("%w: the first report of a session creates its work item and needs a title (--title) or a goal (--goal)", jobstore.ErrWorkInvalid)
	}
	by := s.normalizeBy(strings.TrimSpace(in.By))
	if by == "" {
		by = s.SessionBy(sid)
	}
	w, err := s.store.CreateWorkItem(jobstore.WorkItemInput{
		Title: title, Goal: strings.TrimSpace(in.Goal), ProjectKey: a.ProjectKey, Workspace: a.Cwd,
		SessionIDs: []string{sid}, Source: jobstore.WorkOriginReport, By: by,
		Note: i18n.T(s.lang(a.ProjectKey), "work.journal.created_by_report", "sid", shortSessionID(sid)),
	})
	if err != nil {
		return jobstore.WorkItem{}, false, err
	}
	w, err = s.Report(w.ID, in.ReportInput)
	if err != nil {
		return jobstore.WorkItem{}, true, err
	}
	s.SyncItem(w.ID)
	if cur, found, _ := s.store.GetWorkItem(w.ID); found {
		w = cur
	}
	return w, true, nil
}

func shortSessionID(sid string) string {
	if len(sid) > 8 {
		return sid[:8]
	}
	return sid
}

// ArchiveResult is what ArchiveAuto did.
type ArchiveResult struct {
	// Archived is the number of work items closed by this call (0 for a dry run).
	Archived int `json:"archived"`
	// IDs are the items closed, or — for a dry run — the ones that would be.
	IDs    []string `json:"ids"`
	DryRun bool     `json:"dry_run,omitempty"`
}

// ArchiveAuto is the one-shot cleanup of the old auto drafts: it archives (status dropped,
// a journal line saying why, one audit row per item) every open item that was generated
// automatically and never touched by a person (Store.ListAutoArchivableWorkItems). allow
// narrows it to a caller's projects (nil = all). Running it again finds nothing: the items
// it closed are no longer open, so the call is idempotent. dryRun only lists what would go.
func (s *Service) ArchiveAuto(by string, allow func(project string) bool, dryRun bool) (ArchiveResult, error) {
	by = s.normalizeBy(by)
	res := ArchiveResult{IDs: []string{}, DryRun: dryRun}
	items, err := s.store.ListAutoArchivableWorkItems()
	if err != nil {
		return res, err
	}
	for _, w := range items {
		if allow != nil && !allow(w.ProjectKey) {
			continue
		}
		if dryRun {
			res.IDs = append(res.IDs, w.ID)
			continue
		}
		st := jobstore.WorkDropped
		// expectedRev: an item somebody edited since the listing is theirs now — skip it.
		if _, _, err := s.store.UpdateWorkItem(w.ID, jobstore.WorkItemPatch{Status: &st, Quiet: true}, w.Rev, by); err != nil {
			continue
		}
		if _, err := s.store.AppendWorkJournal(w.ID, jobstore.WorkJournalStatus,
			i18n.T(s.lang(w.ProjectKey), "work.journal.archived_auto"), by); err != nil {
			return res, err
		}
		if _, err := s.store.AppendAuditEvent(jobstore.WorkArchivedAutoEvent, w.ID, by, ""); err != nil {
			return res, err
		}
		res.IDs = append(res.IDs, w.ID)
	}
	if !dryRun {
		res.Archived = len(res.IDs)
	}
	return res, nil
}
