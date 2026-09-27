package tracker

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Store struct{ Dir string }

func NewStore(dir string) *Store { return &Store{Dir: dir} }

func (s *Store) ReadIssues() ([]Issue, error) {
	var items []Issue
	err := readLines(filepath.Join(s.Dir, "issues.jsonl"), func(line []byte) error {
		var item Issue
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if item.ID == "" {
			return errors.New("issue without id")
		}
		items = append(items, item)
		return nil
	})
	return items, err
}

func (s *Store) WriteIssues(items []Issue) (err error) {
	lock, err := s.AcquireLock()
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := lock.Release(); err == nil {
			err = releaseErr
		}
	}()
	return s.writeIssues(items)
}

func (s *Store) UpdateIssues(change func([]Issue) ([]Issue, error)) (err error) {
	lock, err := s.AcquireLock()
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := lock.Release(); err == nil {
			err = releaseErr
		}
	}()
	items, err := s.ReadIssues()
	if err != nil {
		return err
	}
	items, err = change(items)
	if err != nil {
		return err
	}
	return s.writeIssues(items)
}

func (s *Store) writeIssues(items []Issue) error {
	copyItems := append([]Issue(nil), items...)
	sort.Slice(copyItems, func(i, j int) bool { return copyItems[i].ID < copyItems[j].ID })
	for i, item := range copyItems {
		if item.ID == "" || (i > 0 && copyItems[i-1].ID == item.ID) {
			return fmt.Errorf("empty or duplicate issue id %q", item.ID)
		}
	}
	return writeLines(filepath.Join(s.Dir, "issues.jsonl"), copyItems)
}

func (s *Store) ReadMemories() ([]Memory, error) {
	var items []Memory
	err := readLines(filepath.Join(s.Dir, "memories.jsonl"), func(line []byte) error {
		var item Memory
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		if item.Key == "" {
			return errors.New("memory without key")
		}
		items = append(items, item)
		return nil
	})
	return items, err
}

func (s *Store) UpdateMemories(change func([]Memory) ([]Memory, error)) (err error) {
	lock, err := s.AcquireLock()
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := lock.Release(); err == nil {
			err = releaseErr
		}
	}()
	items, err := s.ReadMemories()
	if err != nil {
		return err
	}
	items, err = change(items)
	if err != nil {
		return err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	for i, item := range items {
		if item.Key == "" || (i > 0 && items[i-1].Key == item.Key) {
			return fmt.Errorf("empty or duplicate memory key %q", item.Key)
		}
	}
	return writeLines(filepath.Join(s.Dir, "memories.jsonl"), items)
}

func readLines(path string, each func([]byte) error) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for lineNo := 1; ; lineNo++ {
		line, err := r.ReadBytes('\n')
		if err == io.EOF && len(line) == 0 {
			return nil
		}
		if err != nil && err != io.EOF {
			return err
		}
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			if e := each(line); e != nil {
				return fmt.Errorf("%s:%d: %w", path, lineNo, e)
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}

func writeLines[T any](path string, items []T) error {
	var out bytes.Buffer
	for _, item := range items {
		line, err := json.Marshal(item)
		if err != nil {
			return err
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	return atomicWrite(path, out.Bytes())
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tracker-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %s: %w", strings.TrimSpace(path), err)
	}
	return nil
}
