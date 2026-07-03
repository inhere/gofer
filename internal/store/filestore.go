package store

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// maxPerJobLogBytes bounds a single per-job log file (stdout.log / stderr.log).
// When a write would push the live file past this size it is rotated: the
// current file is renamed to "<name>.1" (overwriting any prior .1) and a fresh
// file is started from 0 (C4, plan §6.2). The default is intentionally large so
// normal jobs never rotate; it is a package var (not a const) so tests can set a
// tiny threshold without writing 500MB of data.
var maxPerJobLogBytes int64 = 500 << 20 // 500 MiB

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
//
// The returned writer is rotation-aware (C4): it tracks bytes written and, once
// the live file would exceed maxPerJobLogBytes, rotates the current file to
// "<name>.1" (overwriting any prior .1) and continues from a fresh file at 0.
// This bounds a single job's on-disk log so a runaway producer cannot grow one
// file without limit. Normal jobs stay well under the threshold and never rotate.
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
	return &rotatingWriter{path: path, f: f}, nil
}

// rotatingWriter wraps an *os.File for a per-job log stream and rotates it when
// the live file would exceed maxPerJobLogBytes. Rotation renames the current
// file to "<path>.1" (overwriting any prior .1) and reopens a fresh truncated
// file at offset 0. A single job's stdout (or stderr) writer is driven by one
// goroutine, but the mutex keeps Write/Close safe under concurrent access.
type rotatingWriter struct {
	path string

	mu      sync.Mutex
	f       *os.File
	written int64 // bytes written to the current (live) file
}

// Write appends p, rotating first if the live file already reached the
// threshold. The whole chunk is written to a single file (no mid-chunk split):
// the threshold is a soft cap so a final oversize write may briefly exceed it,
// then the next write triggers rotation. No bytes are ever dropped.
func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.written >= maxPerJobLogBytes {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.written += int64(n)
	return n, err
}

// rotateLocked renames the live file to "<path>.1" and opens a fresh file at 0.
// Caller must hold w.mu.
func (w *rotatingWriter) rotateLocked() error {
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("close before rotate: %w", err)
	}
	rotated := w.path + ".1"
	// Rename overwrites any existing .1 (only the most recent prior segment is
	// retained), bounding total per-stream disk use at ~2x the threshold.
	if err := os.Rename(w.path, rotated); err != nil {
		return fmt.Errorf("rotate log: %w", err)
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("reopen log after rotate: %w", err)
	}
	w.f = f
	w.written = 0
	return nil
}

// Close closes the underlying live file.
func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
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

// ReadLogLines returns a line window counted from the end of the stream's log
// file. offset skips that many logical lines from the tail, then lines chooses
// how many earlier lines to return. When lines <= 0 the whole file is returned.
// A missing log file yields an empty result and totalLines=0.
func (s *FileStore) ReadLogLines(jobID string, stream Stream, lines, offset int) ([]byte, int, error) {
	name, err := logFileName(stream)
	if err != nil {
		return nil, 0, err
	}
	path := filepath.Join(s.Dir(jobID), name)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []byte{}, 0, nil
		}
		return nil, 0, err
	}

	totalLines := countLogicalLines(data)
	if totalLines == 0 || lines <= 0 {
		return data, totalLines, nil
	}
	if offset < 0 {
		offset = 0
	}
	endLine := totalLines - offset
	if endLine <= 0 {
		return []byte{}, totalLines, nil
	}
	startLine := endLine - lines
	if startLine < 0 {
		startLine = 0
	}

	starts := lineStartOffsets(data, totalLines)
	startByte := starts[startLine]
	endByte := len(data)
	if endLine < totalLines {
		endByte = starts[endLine]
	}
	return data[startByte:endByte], totalLines, nil
}

func countLogicalLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := 0
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

func lineStartOffsets(data []byte, totalLines int) []int {
	starts := make([]int, 0, totalLines)
	if totalLines == 0 {
		return starts
	}
	starts = append(starts, 0)
	for i, b := range data {
		if b == '\n' && i+1 < len(data) {
			starts = append(starts, i+1)
		}
	}
	return starts
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
