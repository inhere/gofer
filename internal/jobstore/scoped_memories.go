package jobstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
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
	scope, scopeKey, err := NormalizeScopedMemoryScope(scope, scopeKey)
	if err != nil {
		return ScopedMemory{}, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return ScopedMemory{}, errors.New("memory key required")
	}
	tags = normalizeScopedMemoryTags(tags)
	rawTags, err := json.Marshal(tags)
	if err != nil {
		return ScopedMemory{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.db.Exec(`INSERT INTO scoped_memories(scope,scope_key,key,content,tags_json,updated_at,updated_by,deleted)
VALUES(?,?,?,?,?,?,?,0)
ON CONFLICT(scope,scope_key,key) DO UPDATE SET content=excluded.content,tags_json=excluded.tags_json,updated_at=excluded.updated_at,updated_by=excluded.updated_by,deleted=0`,
		scope, scopeKey, key, content, string(rawTags), now, updatedBy)
	if err != nil {
		return ScopedMemory{}, err
	}
	return ScopedMemory{Scope: scope, ScopeKey: scopeKey, Key: key, Content: content, Tags: tags, UpdatedAt: now, UpdatedBy: updatedBy}, nil
}

func (s *Store) ListScopedMemories(scope, scopeKey, keyword string, tags []string) ([]ScopedMemory, error) {
	scope, scopeKey, err := NormalizeScopedMemoryScope(scope, scopeKey)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT key,content,tags_json,updated_at,updated_by,deleted FROM scoped_memories WHERE scope=? AND scope_key=? AND deleted=0 ORDER BY updated_at DESC,key`, scope, scopeKey)
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
	var row *sql.Row = s.db.QueryRow(`SELECT key,content,tags_json,updated_at,updated_by,deleted FROM scoped_memories WHERE scope=? AND scope_key=? AND key=?`, scope, scopeKey, strings.TrimSpace(key))
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
	var rawTags string
	var deleted int
	err := row.Scan(&item.Key, &item.Content, &rawTags, &item.UpdatedAt, &item.UpdatedBy, &deleted)
	if err != nil {
		return ScopedMemory{}, err
	}
	item.Scope, item.ScopeKey, item.Deleted = scope, scopeKey, deleted != 0
	if rawTags != "" {
		_ = json.Unmarshal([]byte(rawTags), &item.Tags)
	}
	item.Tags = normalizeScopedMemoryTags(item.Tags)
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
