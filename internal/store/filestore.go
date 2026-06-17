package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FileStore is a filesystem-backed Store rooted at a base directory. Each job's
// artifacts live under <base>/<job_id>/.
type FileStore struct {
	base string
}

// NewFileStore returns a FileStore whose per-job directories are created under
// base. base is not created here; Ensure creates each job dir on demand.
func NewFileStore(base string) *FileStore {
	return &FileStore{base: base}
}

// Dir returns the absolute result directory for jobID.
func (s *FileStore) Dir(jobID string) string {
	return filepath.Join(s.base, jobID)
}

// Ensure creates the result directory for jobID. It first MkdirAll's the parent
// base, then creates the job dir with os.Mkdir so an existing dir is reported as
// a collision (ErrExist) — the job service relies on this to retry the id.
func (s *FileStore) Ensure(jobID string) error {
	if err := os.MkdirAll(s.base, 0o755); err != nil {
		return fmt.Errorf("mkdir result base: %w", err)
	}
	if err := os.Mkdir(s.Dir(jobID), 0o755); err != nil {
		return fmt.Errorf("create job dir: %w", err)
	}
	return nil
}

// WriteRequest persists req as request.json (pretty-printed for inspection).
func (s *FileStore) WriteRequest(jobID string, req any) error {
	return s.writeJSON(filepath.Join(s.Dir(jobID), RequestFile), req)
}

// LogWriter opens the stdout/stderr log file for streaming writes, truncating
// any previous content. The caller closes the returned writer.
func (s *FileStore) LogWriter(jobID string, stream Stream) (io.WriteCloser, error) {
	name, err := logFileName(stream)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(s.Dir(jobID), name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	return f, nil
}

// WriteResult atomically writes result.json: it serialises to a temp file in the
// same directory then renames it over result.json, so readers never observe a
// half-written JSON document.
func (s *FileStore) WriteResult(jobID string, result any) error {
	dir := s.Dir(jobID)
	final := filepath.Join(dir, ResultFile)
	tmp := final + ".tmp"

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write tmp result: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename result: %w", err)
	}
	return nil
}

// ReadResult decodes result.json into out.
func (s *FileStore) ReadResult(jobID string, out any) error {
	data, err := os.ReadFile(filepath.Join(s.Dir(jobID), ResultFile))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// ReadLogTail returns the last maxBytes of the stream's log file. When maxBytes
// <= 0 the whole file is returned. A missing log file yields an empty result
// with no error (a job may not have produced output on that stream yet).
func (s *FileStore) ReadLogTail(jobID string, stream Stream, maxBytes int64) ([]byte, error) {
	name, err := logFileName(stream)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(s.Dir(jobID), name)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []byte{}, nil
		}
		return nil, err
	}
	defer f.Close()

	if maxBytes <= 0 {
		return io.ReadAll(f)
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() > maxBytes {
		if _, err := f.Seek(-maxBytes, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(f)
}

// AppendIndex appends rec as one JSON line to <base>/jobs.jsonl. A single
// Write of the marshalled line plus newline under O_APPEND is atomic on POSIX,
// so concurrent appends never interleave bytes within a line; the job service
// additionally serialises calls to keep whole lines ordered.
func (s *FileStore) AppendIndex(rec any) error {
	path := filepath.Join(s.base, IndexFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		// base may not exist yet in edge cases; create it once and retry.
		if os.IsNotExist(err) {
			if mkErr := os.MkdirAll(s.base, 0o755); mkErr != nil {
				return mkErr
			}
			f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		}
		if err != nil {
			return err
		}
	}
	defer f.Close()
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n')) // single write, O_APPEND atomic append
	return err
}

// ReadIndex returns every valid JSON line in <base>/jobs.jsonl as a
// json.RawMessage. A missing file yields an empty slice with no error. Empty
// and corrupt (non-JSON) lines are skipped so a partially written tail never
// breaks the read path.
func (s *FileStore) ReadIndex() ([]json.RawMessage, error) {
	f, err := os.Open(filepath.Join(s.base, IndexFile))
	if err != nil {
		if os.IsNotExist(err) {
			return []json.RawMessage{}, nil
		}
		return nil, err
	}
	defer f.Close()

	out := []json.RawMessage{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			continue // tolerate a corrupt/half-written line
		}
		// Copy: Scanner reuses its buffer between Scan calls, so the slice would
		// otherwise alias and be overwritten on the next iteration.
		out = append(out, append([]byte(nil), line...))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// AppendInteraction appends rec as one JSON line to
// <base>/<jobID>/interactions.jsonl. Like AppendIndex, a single Write of the
// marshalled line plus newline under O_APPEND is atomic on POSIX, so concurrent
// appends never interleave bytes within a line; callers additionally serialise
// to keep whole lines ordered. Unlike AppendIndex the file lives inside the
// single job directory, not the project base.
func (s *FileStore) AppendInteraction(jobID string, rec any) error {
	dir := s.Dir(jobID)
	path := filepath.Join(dir, InteractionsFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		// The job dir may not exist yet in edge cases; create it once and retry.
		if os.IsNotExist(err) {
			if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
				return mkErr
			}
			f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		}
		if err != nil {
			return err
		}
	}
	defer f.Close()
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n')) // single write, O_APPEND atomic append
	return err
}

// ReadInteractions returns every valid JSON line in
// <base>/<jobID>/interactions.jsonl as a json.RawMessage. A missing file yields
// an empty slice with no error. Empty and corrupt (non-JSON) lines are skipped
// so a partially written tail never breaks the read path.
func (s *FileStore) ReadInteractions(jobID string) ([]json.RawMessage, error) {
	f, err := os.Open(filepath.Join(s.Dir(jobID), InteractionsFile))
	if err != nil {
		if os.IsNotExist(err) {
			return []json.RawMessage{}, nil
		}
		return nil, err
	}
	defer f.Close()

	out := []json.RawMessage{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			continue // tolerate a corrupt/half-written line
		}
		// Copy: Scanner reuses its buffer between Scan calls, so the slice would
		// otherwise alias and be overwritten on the next iteration.
		out = append(out, append([]byte(nil), line...))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// writeJSON pretty-prints v to path (non-atomic; used for request.json which is
// written once at creation before any reader exists).
func (s *FileStore) writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", filepath.Base(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}

// logFileName maps a stream to its on-disk file name.
func logFileName(stream Stream) (string, error) {
	switch stream {
	case StreamStdout:
		return StdoutFile, nil
	case StreamStderr:
		return StderrFile, nil
	default:
		return "", fmt.Errorf("unknown log stream %q", stream)
	}
}
