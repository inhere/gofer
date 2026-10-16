package jobstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/tracker"
)

// Server-created tracker issues.
//
// The server can file an issue into a repository's mirror (a captured item turned into an
// issue, a job finding). The repository only gets it on its next `repo sync`, and until
// then the repository can independently allocate the same random id. tracker_issues carries
// a server_created flag for exactly that window:
//
//   - set when the server creates the issue (CreateServerTrackerIssue);
//   - cleared when a repository has pulled it: a sync request whose issue_since cursor is at
//     or past the record's changed_seq proves the client received it
//     (ClearServerCreatedPulled), or when a client pushes the record back with a base
//     revision that already includes it (syncTracker's upsert);
//   - consulted by syncTracker: a pushed NEW issue (base rev 0) with the same id as a flagged
//     record renames the server's record instead of merging the two.

// ErrTrackerNoPrefix means the repository has no issue id prefix recorded yet.
var ErrTrackerNoPrefix = errors.New("tracker repository has no issue id prefix recorded yet")

// ErrTrackerRepoUnknown means no repository is registered under the tracker id.
var ErrTrackerRepoUnknown = errors.New("tracker repository is not registered on this server")

// TrackerRename records one id change made while accepting a push.
type TrackerRename struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// CreateServerTrackerIssue allocates an id (tracker.GenerateIssueID over the mirror's
// issues and the repository prefix), stores the issue as a new rev-1 record flagged
// server_created and returns it. Deps and parent are cleared: a server-created issue has
// none. Lookup, allocation and insert share one transaction.
func (s *Store) CreateServerTrackerIssue(trackerID string, in tracker.Issue, by string) (TrackerRecord, error) {
	if trackerID == "" {
		return TrackerRecord{}, fmt.Errorf("tracker_id required")
	}
	if IsReservedTracker(trackerID) {
		return TrackerRecord{}, ErrReservedTracker
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return TrackerRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var prefix string
	if err = tx.QueryRow(`SELECT prefix FROM tracker_repos WHERE tracker_id=?`, trackerID).Scan(&prefix); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrTrackerRepoUnknown
		}
		return TrackerRecord{}, err
	}
	if strings.TrimSpace(prefix) == "" {
		err = ErrTrackerNoPrefix
		return TrackerRecord{}, err
	}
	existing, err := trackerIssuesOn(tx, trackerID)
	if err != nil {
		return TrackerRecord{}, err
	}
	id, err := tracker.GenerateIssueID(existing, prefix, "")
	if err != nil {
		return TrackerRecord{}, err
	}
	now := tracker.Now()
	in.ID, in.Parent, in.Deps = id, "", nil
	in.CreatedAt, in.UpdatedAt, in.CreatedBy = now, now, by
	if in.Status == "" {
		in.Status = "open"
	}
	if in.Type == "" {
		in.Type = "task"
	}
	body, _ := json.Marshal(in)
	var seq int64
	if err = tx.QueryRow(`UPDATE tracker_repos SET next_seq=next_seq+1 WHERE tracker_id=? RETURNING next_seq`, trackerID).Scan(&seq); err != nil {
		return TrackerRecord{}, err
	}
	if _, err = tx.Exec(`INSERT INTO tracker_issues(tracker_id,issue_id,body_json,rev,updated_at,changed_seq,server_created) VALUES(?,?,?,?,?,?,1)`, trackerID, id, string(body), 1, now, seq); err != nil {
		return TrackerRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return TrackerRecord{}, err
	}
	return TrackerRecord{TrackerID: trackerID, ID: id, Body: body, Rev: 1, UpdatedAt: now, ChangedSeq: seq, ServerCreated: true}, nil
}

// trackerIssuesOn lists the mirror's issues (id only is enough for allocation, the rest is
// best effort) of one repository.
func trackerIssuesOn(q execer, trackerID string) ([]tracker.Issue, error) {
	rows, err := q.Query(`SELECT issue_id, body_json FROM tracker_issues WHERE tracker_id=?`, trackerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tracker.Issue
	for rows.Next() {
		var id, body string
		if err := rows.Scan(&id, &body); err != nil {
			return nil, err
		}
		var is tracker.Issue
		_ = json.Unmarshal([]byte(body), &is)
		is.ID = id
		out = append(out, is)
	}
	return out, rows.Err()
}

// ClearServerCreatedPulled clears the server_created flag of every record the client
// holding cursor has received (changed_seq <= cursor). cursor 0 clears nothing: a first
// sync pulls with no cursor and never proves a delivery.
func (s *Store) ClearServerCreatedPulled(trackerID string, cursor int64) error {
	if trackerID == "" || cursor <= 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE tracker_issues SET server_created=0 WHERE tracker_id=? AND server_created=1 AND changed_seq<=?`, trackerID, cursor)
	return err
}

// renameServerIssueTx gives the flagged record oldID a fresh id inside tx and keeps every
// reference to it consistent: the parent / deps fields of the other mirrored issues of the
// repository and the issue links of work items. The renamed record gets a note, a new rev
// and a new change sequence (so clones pull it under the new id). It returns the rename,
// the renamed record and the work items whose links were rewritten (journaled by the
// caller after commit via the returned ids).
func (s *Store) renameServerIssueTx(tx *sql.Tx, trackerID, oldID, now string) (TrackerRename, TrackerRecord, []string, error) {
	var prefix string
	_ = tx.QueryRow(`SELECT prefix FROM tracker_repos WHERE tracker_id=?`, trackerID).Scan(&prefix)
	if strings.TrimSpace(prefix) == "" {
		if i := strings.LastIndex(oldID, "-"); i > 0 {
			prefix = oldID[:i]
		}
	}
	existing, err := trackerIssuesOn(tx, trackerID)
	if err != nil {
		return TrackerRename{}, TrackerRecord{}, nil, err
	}
	newID, err := tracker.GenerateIssueID(existing, prefix, "")
	if err != nil {
		return TrackerRename{}, TrackerRecord{}, nil, err
	}
	var body string
	var rev int64
	var createdAt string
	if err = tx.QueryRow(`SELECT body_json, rev FROM tracker_issues WHERE tracker_id=? AND issue_id=?`, trackerID, oldID).Scan(&body, &rev); err != nil {
		return TrackerRename{}, TrackerRecord{}, nil, err
	}
	obj := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(body), &obj)
	obj["id"], _ = json.Marshal(newID)
	obj["updated_at"], _ = json.Marshal(now)
	var notes []tracker.NoteEntry
	_ = json.Unmarshal(obj["notes"], &notes)
	notes = append(notes, tracker.NoteEntry{At: now, By: "gofer", Text: fmt.Sprintf("id renamed from %s to %s: a repository issue with the same id was pushed before this server-created issue was pulled", oldID, newID)})
	obj["notes"], _ = json.Marshal(notes)
	_ = json.Unmarshal(obj["created_at"], &createdAt)
	out, _ := json.Marshal(obj)
	seq, err := nextTrackerSeq(tx, trackerID)
	if err != nil {
		return TrackerRename{}, TrackerRecord{}, nil, err
	}
	rev++
	if _, err = tx.Exec(`UPDATE tracker_issues SET issue_id=?, body_json=?, rev=?, updated_at=?, changed_seq=? WHERE tracker_id=? AND issue_id=?`, newID, string(out), rev, now, seq, trackerID, oldID); err != nil {
		return TrackerRename{}, TrackerRecord{}, nil, err
	}
	if err = s.rewriteIssueRefsTx(tx, trackerID, oldID, newID, now); err != nil {
		return TrackerRename{}, TrackerRecord{}, nil, err
	}
	items, err := s.renameWorkLinksTx(tx, oldID, newID, createdAt, now)
	if err != nil {
		return TrackerRename{}, TrackerRecord{}, nil, err
	}
	rec := TrackerRecord{TrackerID: trackerID, ID: newID, Body: out, Rev: rev, UpdatedAt: now, ChangedSeq: seq, ServerCreated: true}
	return TrackerRename{From: oldID, To: newID}, rec, items, nil
}

func nextTrackerSeq(tx *sql.Tx, trackerID string) (seq int64, err error) {
	err = tx.QueryRow(`UPDATE tracker_repos SET next_seq=next_seq+1 WHERE tracker_id=? RETURNING next_seq`, trackerID).Scan(&seq)
	return
}

// rewriteIssueRefsTx rewrites parent / deps references to oldID inside one repository
// mirror (bumping rev and change sequence of each touched issue).
func (s *Store) rewriteIssueRefsTx(tx *sql.Tx, trackerID, oldID, newID, now string) error {
	rows, err := tx.Query(`SELECT issue_id, body_json, rev FROM tracker_issues WHERE tracker_id=? AND issue_id<>?`, trackerID, newID)
	if err != nil {
		return err
	}
	type edit struct {
		id   string
		body []byte
		rev  int64
	}
	var edits []edit
	for rows.Next() {
		var id, body string
		var rev int64
		if err := rows.Scan(&id, &body, &rev); err != nil {
			rows.Close()
			return err
		}
		obj := map[string]json.RawMessage{}
		if json.Unmarshal([]byte(body), &obj) != nil {
			continue
		}
		changed := false
		var parent string
		if json.Unmarshal(obj["parent"], &parent) == nil && parent == oldID {
			obj["parent"], _ = json.Marshal(newID)
			changed = true
		}
		var deps []tracker.Dep
		if json.Unmarshal(obj["deps"], &deps) == nil {
			for i := range deps {
				if deps[i].ID == oldID {
					deps[i].ID = newID
					changed = true
				}
			}
			if changed {
				obj["deps"], _ = json.Marshal(deps)
			}
		}
		if !changed {
			continue
		}
		obj["updated_at"], _ = json.Marshal(now)
		out, _ := json.Marshal(obj)
		edits = append(edits, edit{id: id, body: out, rev: rev + 1})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, e := range edits {
		seq, err := nextTrackerSeq(tx, trackerID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE tracker_issues SET body_json=?, rev=?, updated_at=?, changed_seq=? WHERE tracker_id=? AND issue_id=?`, string(e.body), e.rev, now, seq, trackerID, e.id); err != nil {
			return err
		}
	}
	return nil
}

// renameWorkLinksTx points the issue links of work items from oldID to newID and journals
// the change on each item. Links are keyed by the bare issue id (not by repository), so a
// link counts only when it was made after the server-created issue existed (createdAt, an
// RFC3339 timestamp; unparsable = every link of that id).
func (s *Store) renameWorkLinksTx(tx *sql.Tx, oldID, newID, createdAt, now string) ([]string, error) {
	since := int64(0)
	if t, ok := parseTrackerTime(createdAt); ok {
		since = t
	}
	rows, err := tx.Query(`SELECT work_item_id FROM work_links WHERE kind=? AND ref=? AND created_at>=?`, WorkLinkIssue, oldID, since)
	if err != nil {
		return nil, err
	}
	var items []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	at := s.unixNow()
	for _, id := range items {
		// A work item that already links the new id (impossible for a fresh id, but the key
		// is (item, kind, ref)) just loses the old row.
		if _, err := tx.Exec(`UPDATE OR IGNORE work_links SET ref=? WHERE work_item_id=? AND kind=? AND ref=?`, newID, id, WorkLinkIssue, oldID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DELETE FROM work_links WHERE work_item_id=? AND kind=? AND ref=?`, id, WorkLinkIssue, oldID); err != nil {
			return nil, err
		}
		if _, err := s.appendWorkJournalOn(tx, id, WorkJournalNote,
			fmt.Sprintf("issue id 已改名 %s -> %s：仓库同步时推送了同 id 的新 issue", oldID, newID), "system", at, ""); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE work_items SET last_activity_at=?, rev=rev+1 WHERE id=?`, at, id); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func parseTrackerTime(v string) (int64, bool) {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return 0, false
	}
	return t.Unix(), true
}
