package jobstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/tracker"
)

const (
	ScopedMemoryGlobal  = "global"
	ScopedMemoryProject = "project"
)

var ErrScopedMemoryNotFound = errors.New("scoped memory not found")

type ScopedMemory struct {
	Scope     string   `json:"scope"`
	ScopeKey  string   `json:"scope_key,omitempty"`
	Key       string   `json:"key"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags,omitempty"`
	UpdatedAt string   `json:"updated_at"`
	UpdatedBy string   `json:"updated_by,omitempty"`
	Deleted   bool     `json:"deleted,omitempty"`
	// MemoryMeta (kind / summary / when / expires_at / source / created_at) is
	// stored as JSON in scoped_memories.meta_json and flattened on the wire.
	tracker.MemoryMeta
}

// ScopedMemoryMetaPatch carries the optional fields of a scoped memory write.
// Nil keeps the stored value (so web / MCP writes that only send content and
// tags never drop them); When replaces the whole trigger set.
type ScopedMemoryMetaPatch struct {
	Kind      *string
	Summary   *string
	When      *tracker.MemoryWhen
	ExpiresAt *string
	Source    *string
}

func NormalizeScopedMemoryScope(scope, scopeKey string) (string, string, error) {
	scope = strings.TrimSpace(scope)
	scopeKey = strings.TrimSpace(scopeKey)
	switch scope {
	case ScopedMemoryGlobal:
		if scopeKey != "" {
			return "", "", fmt.Errorf("global memory scope_key must be empty")
		}
	case ScopedMemoryProject:
		if scopeKey == "" {
			return "", "", fmt.Errorf("project memory scope_key is required")
		}
	default:
		return "", "", fmt.Errorf("invalid memory scope %q", scope)
	}
	return scope, scopeKey, nil
}

func normalizeScopedMemoryTags(tags []string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

func (s *Store) PutScopedMemory(scope, scopeKey, key, content string, tags []string, updatedBy string) (ScopedMemory, error) {
	return s.PutScopedMemoryPatch(scope, scopeKey, key, content, tags, ScopedMemoryMetaPatch{}, updatedBy)
}

// PutScopedMemoryPatch upserts a scoped memory. Content and tags are replaced as
// before; the meta fields follow tracker.ApplyMemoryPatch (nil keeps).
func (s *Store) PutScopedMemoryPatch(scope, scopeKey, key, content string, tags []string, meta ScopedMemoryMetaPatch, updatedBy string) (ScopedMemory, error) {
	scope, scopeKey, err := NormalizeScopedMemoryScope(scope, scopeKey)
	if err != nil {
		return ScopedMemory{}, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return ScopedMemory{}, errors.New("memory key required")
	}
	tags = normalizeScopedMemoryTags(tags)
	if tags == nil {
		tags = []string{}
	}
	rawTags, err := json.Marshal(tags)
	if err != nil {
		return ScopedMemory{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var existing *tracker.Memory
	if old, getErr := s.GetScopedMemory(scope, scopeKey, key); getErr == nil && !old.Deleted {
		existing = &tracker.Memory{Key: old.Key, Content: old.Content, Tags: old.Tags, UpdatedAt: old.UpdatedAt, By: old.UpdatedBy, MemoryMeta: old.MemoryMeta}
	} else if getErr != nil && !errors.Is(getErr, ErrScopedMemoryNotFound) {
		return ScopedMemory{}, getErr
	}
	patch := tracker.MemoryPatch{Content: content, Tags: tags, Kind: meta.Kind, Summary: meta.Summary, Source: meta.Source, ExpiresAt: meta.ExpiresAt, By: updatedBy}
	if meta.When != nil {
		patch.WhenKeywords, patch.WhenPaths, patch.WhenCommands = &meta.When.Keywords, &meta.When.Paths, &meta.When.Commands
	}
	merged, err := tracker.ApplyMemoryPatch(existing, key, patch, time.Now())
	if err != nil {
		return ScopedMemory{}, err
	}
	rawMeta, err := json.Marshal(merged.MemoryMeta)
	if err != nil {
		return ScopedMemory{}, err
	}
	now := merged.UpdatedAt
	_, err = s.db.Exec(`INSERT INTO scoped_memories(scope,scope_key,key,content,tags_json,updated_at,updated_by,deleted,meta_json)
VALUES(?,?,?,?,?,?,?,0,?)
ON CONFLICT(scope,scope_key,key) DO UPDATE SET content=excluded.content,tags_json=excluded.tags_json,updated_at=excluded.updated_at,updated_by=excluded.updated_by,deleted=0,meta_json=excluded.meta_json`,
		scope, scopeKey, key, content, string(rawTags), now, updatedBy, string(rawMeta))
	if err != nil {
		return ScopedMemory{}, err
	}
	return ScopedMemory{Scope: scope, ScopeKey: scopeKey, Key: key, Content: content, Tags: normalizeScopedMemoryTags(tags), UpdatedAt: now, UpdatedBy: updatedBy, MemoryMeta: merged.MemoryMeta}, nil
}

func (s *Store) ListScopedMemories(scope, scopeKey, keyword string, tags []string) ([]ScopedMemory, error) {
	scope, scopeKey, err := NormalizeScopedMemoryScope(scope, scopeKey)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT key,content,tags_json,updated_at,updated_by,deleted,meta_json FROM scoped_memories WHERE scope=? AND scope_key=? AND deleted=0 ORDER BY updated_at DESC,key`, scope, scopeKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	wantKeyword := strings.ToLower(strings.TrimSpace(keyword))
	wantTags := normalizeScopedMemoryTags(tags)
	var out []ScopedMemory
	for rows.Next() {
		item, err := scanScopedMemory(rows, scope, scopeKey)
		if err != nil {
			return nil, err
		}
		if wantKeyword != "" && !strings.Contains(strings.ToLower(item.Key+" "+item.Content), wantKeyword) {
			continue
		}
		if !scopedMemoryHasTags(item.Tags, wantTags) {
			continue
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) GetScopedMemory(scope, scopeKey, key string) (ScopedMemory, error) {
	scope, scopeKey, err := NormalizeScopedMemoryScope(scope, scopeKey)
	if err != nil {
		return ScopedMemory{}, err
	}
	var row *sql.Row = s.db.QueryRow(`SELECT key,content,tags_json,updated_at,updated_by,deleted,meta_json FROM scoped_memories WHERE scope=? AND scope_key=? AND key=?`, scope, scopeKey, strings.TrimSpace(key))
	item, err := scanScopedMemory(row, scope, scopeKey)
	if errors.Is(err, sql.ErrNoRows) {
		return ScopedMemory{}, ErrScopedMemoryNotFound
	}
	return item, err
}

func (s *Store) DeleteScopedMemory(scope, scopeKey, key, updatedBy string) error {
	scope, scopeKey, err := NormalizeScopedMemoryScope(scope, scopeKey)
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("memory key required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE scoped_memories SET deleted=1,updated_at=?,updated_by=? WHERE scope=? AND scope_key=? AND key=?`, now, updatedBy, scope, scopeKey, key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrScopedMemoryNotFound
	}
	return nil
}

type scopedMemoryScanner interface{ Scan(...any) error }

func scanScopedMemory(row scopedMemoryScanner, scope, scopeKey string) (ScopedMemory, error) {
	var item ScopedMemory
	var rawTags, rawMeta string
	var deleted int
	err := row.Scan(&item.Key, &item.Content, &rawTags, &item.UpdatedAt, &item.UpdatedBy, &deleted, &rawMeta)
	if err != nil {
		return ScopedMemory{}, err
	}
	item.Scope, item.ScopeKey, item.Deleted = scope, scopeKey, deleted != 0
	if rawTags != "" {
		_ = json.Unmarshal([]byte(rawTags), &item.Tags)
	}
	item.Tags = normalizeScopedMemoryTags(item.Tags)
	if rawMeta != "" {
		_ = json.Unmarshal([]byte(rawMeta), &item.MemoryMeta)
	}
	return item, nil
}

func scopedMemoryHasTags(have, want []string) bool {
	for _, tag := range want {
		found := false
		for _, got := range have {
			if got == tag {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
