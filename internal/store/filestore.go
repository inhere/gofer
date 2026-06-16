package store

import (
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
