package store

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FileStore is a filesystem-backed Store rooted at a base directory. Each job's
// log artifacts live under <base>/<job_id>/.
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
