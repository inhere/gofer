package jobstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/tracker"
)

// Global memories live in the reserved tracker tracker.GlobalTrackerID
// (docs/design/2026-10-12-local-global-memory-design.md): one tracker_memories set
// with revisions, tombstones and a change cursor, so every machine keeps a local copy
// under <config-dir>/tracker and syncs it with POST /v1/tracker/sync like a repository
// tracker. The scoped-memory API (scope "global") is a view over that set; project
// scope stays in scoped_memories.

// ErrReservedTracker refuses repository-tracker operations on the global tracker.
var ErrReservedTracker = errors.New("the global memory tracker cannot be renamed or deleted")

// IsReservedTracker reports whether trackerID is the global memory tracker.
func IsReservedTracker(trackerID string) bool { return trackerID == tracker.GlobalTrackerID }

// globalMemory reads one global memory (a tombstone comes back with Deleted set) and
// its revision.
func (s *Store) globalMemory(key string) (ScopedMemory, int64, error) {
	var body string
	var rev int64
	var deleted int
	err := s.db.QueryRow(`SELECT body_json,rev,deleted FROM tracker_memories WHERE tracker_id=? AND memory_key=?`,
		tracker.GlobalTrackerID, strings.TrimSpace(key)).Scan(&body, &rev, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return ScopedMemory{}, 0, ErrScopedMemoryNotFound
	}
	if err != nil {
		return ScopedMemory{}, 0, err
	}
	item := globalScoped(body)
	item.Key, item.Deleted = strings.TrimSpace(key), deleted != 0
	return item, rev, nil
}

// globalScoped turns a tracker memory body into the scoped view.
func globalScoped(body string) ScopedMemory {
	var m tracker.Memory
	_ = json.Unmarshal([]byte(body), &m)
	return ScopedMemory{Scope: ScopedMemoryGlobal, Key: m.Key, Content: m.Content, Tags: normalizeScopedMemoryTags(m.Tags),
		UpdatedAt: m.UpdatedAt, UpdatedBy: m.By, MemoryMeta: m.MemoryMeta}
}

func (s *Store) listGlobalMemories(keyword string, tags []string) ([]ScopedMemory, error) {
	rows, err := s.db.Query(`SELECT memory_key,body_json FROM tracker_memories WHERE tracker_id=? AND deleted=0`, tracker.GlobalTrackerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	wantKeyword := strings.ToLower(strings.TrimSpace(keyword))
	wantTags := normalizeScopedMemoryTags(tags)
	var out []ScopedMemory
	for rows.Next() {
		var key, body string
		if err := rows.Scan(&key, &body); err != nil {
			return nil, err
		}
		item := globalScoped(body)
		item.Key = key
		if wantKeyword != "" && !strings.Contains(strings.ToLower(item.Key+" "+item.Content), wantKeyword) {
			continue
		}
		if !scopedMemoryHasTags(item.Tags, wantTags) {
			continue
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// writeGlobalMemoryLocked stores a server-side write of one global memory as the next
// revision with a fresh change sequence, so every local copy pulls it on its next sync.
// A tombstone (deleted) keeps an empty body. The caller holds s.writeMu.
func (s *Store) writeGlobalMemoryLocked(m tracker.Memory, deleted bool, deletedBy string) error {
	body := []byte("{}")
	if !deleted {
		var err error
		if body, err = json.Marshal(m); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = writeGlobalMemoryTx(tx, m.Key, body, now, deleted, deletedBy); err != nil {
		return err
	}
	return tx.Commit()
}

// writeGlobalMemoryTx writes one global memory row as rev+1 inside tx.
func writeGlobalMemoryTx(tx *sql.Tx, key string, body []byte, now string, deleted bool, deletedBy string) error {
	if _, err := tx.Exec(`INSERT INTO tracker_repos(tracker_id) VALUES(?) ON CONFLICT(tracker_id) DO NOTHING`, tracker.GlobalTrackerID); err != nil {
		return err
	}
	var seq int64
	if err := tx.QueryRow(`UPDATE tracker_repos SET next_seq=next_seq+1 WHERE tracker_id=? RETURNING next_seq`, tracker.GlobalTrackerID).Scan(&seq); err != nil {
		return err
	}
	var rev int64
	err := tx.QueryRow(`SELECT rev FROM tracker_memories WHERE tracker_id=? AND memory_key=?`, tracker.GlobalTrackerID, key).Scan(&rev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	deletedAt := ""
	if deleted {
		deletedAt = now
	} else {
		deletedBy = ""
	}
	_, err = tx.Exec(`INSERT INTO tracker_memories(tracker_id,memory_key,body_json,rev,updated_at,deleted,deleted_at,deleted_by,changed_seq) VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(tracker_id,memory_key) DO UPDATE SET body_json=excluded.body_json,rev=excluded.rev,updated_at=excluded.updated_at,deleted=excluded.deleted,deleted_at=excluded.deleted_at,deleted_by=excluded.deleted_by,changed_seq=excluded.changed_seq`,
		tracker.GlobalTrackerID, key, string(body), rev+1, now, boolInt(deleted), deletedAt, deletedBy, seq)
	return err
}

// migrateGlobalScopedMemories moves the global rows of scoped_memories (the storage
// before the global tracker) into it, once: the moved rows are deleted from
// scoped_memories in the same transaction, so a later start finds nothing to move. A
// key the global tracker already holds keeps whichever side was written last.
func (s *Store) migrateGlobalScopedMemories() error {
	rows, err := s.db.Query(`SELECT key,content,tags_json,updated_at,updated_by,deleted,meta_json FROM scoped_memories WHERE scope=?`, ScopedMemoryGlobal)
	if err != nil {
		return err
	}
	var items []ScopedMemory
	for rows.Next() {
		item, err := scanScopedMemory(rows, ScopedMemoryGlobal, "")
		if err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(items) == 0 {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	moved := 0
	for _, item := range items {
		if item.Deleted {
			continue
		}
		var curBody string
		var curDeleted int
		err := tx.QueryRow(`SELECT body_json,deleted FROM tracker_memories WHERE tracker_id=? AND memory_key=?`, tracker.GlobalTrackerID, item.Key).Scan(&curBody, &curDeleted)
		if err == nil && curDeleted == 0 && globalScoped(curBody).UpdatedAt >= item.UpdatedAt {
			continue
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		m := tracker.Memory{Key: item.Key, Content: item.Content, Tags: item.Tags, UpdatedAt: item.UpdatedAt, By: item.UpdatedBy, MemoryMeta: item.MemoryMeta}
		body, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if err := writeGlobalMemoryTx(tx, item.Key, body, item.UpdatedAt, false, ""); err != nil {
			return fmt.Errorf("migrate global memory %q: %w", item.Key, err)
		}
		moved++
	}
	if _, err := tx.Exec(`DELETE FROM scoped_memories WHERE scope=?`, ScopedMemoryGlobal); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if moved > 0 {
		slog.Info("jobstore: moved global memories into the global tracker", "count", moved)
	}
	return nil
}
