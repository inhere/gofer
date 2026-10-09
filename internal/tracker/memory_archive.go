package tracker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// memoryArchiveFile holds archived memories (design §2.6 / §2.10). It is a
// plain repository file: committed with git like memories.jsonl but NOT part of
// the tracker sync. Archiving removes the memory from memories.jsonl, so sync
// pushes a tombstone and other clones drop it; they see the archived copy once
// the archive file reaches them through git.
const memoryArchiveFile = "memories-archive.jsonl"

// ArchivedMemory is a memory moved out of prime: every field is kept (summary,
// kind, when, source …) plus when, by whom and why it was archived.
type ArchivedMemory struct {
	Memory
	ArchivedAt    string `json:"archived_at"`
	ArchivedBy    string `json:"archived_by,omitempty"`
	ArchiveReason string `json:"archive_reason,omitempty"`
}

func parseArchivedMemories(r io.Reader, name string) ([]ArchivedMemory, error) {
	var items []ArchivedMemory
	err := scanLines(r, name, func(line []byte) error {
		var item ArchivedMemory
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if item.Key == "" {
			return errors.New("archived memory without key")
		}
		items = append(items, item)
		return nil
	})
	return items, err
}

// ReadArchivedMemories reads memories-archive.jsonl (missing file = none).
func (s *Store) ReadArchivedMemories() ([]ArchivedMemory, error) {
	return readFileWith(filepath.Join(s.Dir, memoryArchiveFile), parseArchivedMemories)
}

func writeArchivedMemories(dir string, items []ArchivedMemory) error {
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return writeLines(filepath.Join(dir, memoryArchiveFile), items)
}

// updateMemoriesAndArchive changes both files under one lock. The archive is
// written first: a crash in between leaves a memory in both files (visible and
// recoverable) rather than in neither.
func (s *Store) updateMemoriesAndArchive(change func([]Memory, []ArchivedMemory) ([]Memory, []ArchivedMemory, error)) (err error) {
	lock, err := s.AcquireLock()
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := lock.Release(); err == nil {
			err = releaseErr
		}
	}()
	live, err := s.ReadMemories()
	if err != nil {
		return err
	}
	archived, err := s.ReadArchivedMemories()
	if err != nil {
		return err
	}
	live, archived, err = change(live, archived)
	if err != nil {
		return err
	}
	if err := writeArchivedMemories(s.Dir, archived); err != nil {
		return err
	}
	sort.Slice(live, func(i, j int) bool { return live[i].Key < live[j].Key })
	return writeLines(filepath.Join(s.Dir, "memories.jsonl"), live)
}

// ArchiveMemory moves key from memories.jsonl to memories-archive.jsonl. An
// older archived copy of the same key is replaced.
func (s *Store) ArchiveMemory(key, reason, actor string) (ArchivedMemory, error) {
	var out ArchivedMemory
	err := s.updateMemoriesAndArchive(func(live []Memory, archived []ArchivedMemory) ([]Memory, []ArchivedMemory, error) {
		idx := -1
		for i := range live {
			if live[i].Key == key {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, nil, fmt.Errorf("memory %s not found", key)
		}
		out = ArchivedMemory{Memory: live[idx], ArchivedAt: Now(), ArchivedBy: actor, ArchiveReason: strings.TrimSpace(reason)}
		live = append(live[:idx], live[idx+1:]...)
		kept := archived[:0]
		for _, a := range archived {
			if a.Key != key {
				kept = append(kept, a)
			}
		}
		return live, append(kept, out), nil
	})
	return out, err
}

// RestoreMemory moves an archived memory back. updated_at is set to now so the
// restored record wins over the sync tombstone its archiving left on the server
// (created_at keeps the original age).
func (s *Store) RestoreMemory(key, actor string) (Memory, error) {
	var out Memory
	err := s.updateMemoriesAndArchive(func(live []Memory, archived []ArchivedMemory) ([]Memory, []ArchivedMemory, error) {
		for _, m := range live {
			if m.Key == key {
				return nil, nil, fmt.Errorf("memory %s already exists; remove or rename it before restoring", key)
			}
		}
		for i, a := range archived {
			if a.Key != key {
				continue
			}
			out = a.Memory
			out.CreatedAt = MemoryCreatedAt(out.MemoryMeta, out.UpdatedAt)
			out.UpdatedAt = Now()
			if actor != "" {
				out.By = actor
			}
			return append(live, out), append(archived[:i], archived[i+1:]...), nil
		}
		return nil, nil, fmt.Errorf("archived memory %s not found", key)
	})
	return out, err
}

// ListArchivedMemories filters the archive like `memory ls` (keyword over key,
// summary and content; tags; kind).
func (s *Store) ListArchivedMemories(filter MemoryFilter) ([]ArchivedMemory, error) {
	items, err := s.ReadArchivedMemories()
	if err != nil {
		return nil, err
	}
	out := make([]ArchivedMemory, 0, len(items))
	for _, item := range items {
		if MemoryMatchesFilter(item.Memory, filter) {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// ArchivedMemoryListLine is one `memory ls --archived` row.
func ArchivedMemoryListLine(a ArchivedMemory, now time.Time) string {
	line := strings.TrimSuffix(MemoryListLine(a.Memory, now), "\n")
	line += " · 归档于 " + AgeText(a.ArchivedAt, now)
	if a.ArchiveReason != "" {
		line += "（" + a.ArchiveReason + "）"
	}
	return line + "\n"
}

// PromoteMemory turns a memory (typically a handoff) into a long-lived rule or
// note: the kind changes, expires_at is cleared, source and everything else are
// kept. summary (when non-nil) replaces the stored summary; the usual write rule
// applies (rule / note content over 200 chars needs one).
func (s *Store) PromoteMemory(key, kind string, summary *string, actor string) (Memory, error) {
	if kind != MemoryKindRule && kind != MemoryKindNote {
		return Memory{}, fmt.Errorf("promote --kind must be rule or note, got %q", kind)
	}
	existing, err := s.Memory(key)
	if err != nil {
		return Memory{}, err
	}
	return s.SetMemoryPatch(key, MemoryPatch{Content: existing.Content, Kind: &kind, Summary: summary, By: actor}, true)
}
