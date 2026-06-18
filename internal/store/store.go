// Package store persists per-job log artifacts under a single result directory
// (<result_dir>/<job_id>/): the live stdout/stderr logs streamed by the local
// runner. Job metadata, the index and interactions now live in the SQLite store
// (internal/jobstore); from SP5 the request is persisted to the jobs.request_json
// column too, so the store no longer writes request.json/result.json/jobs.jsonl/
// interactions.jsonl. See design §7 / §10 / §14.
//
// Directory layout for one job:
//
//	<result_dir>/<job_id>/
//	  stdout.log          // child process stdout, streamed by the local runner
//	  stderr.log          // child process stderr, streamed by the local runner
package store

import (
	"io"
)

// Artifact file names inside a job result directory. Only the log files remain;
// metadata/index/request/interactions moved to the SQLite store (jobstore).
const (
	StdoutFile = "stdout.log"
	StderrFile = "stderr.log"
)

// Stream identifies which log file to open/read.
type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

// Store abstracts per-job log persistence. FileStore is the only implementation;
// the interface lets the job service and tests depend on behaviour rather than the
// filesystem layout. Metadata/index/interactions live in internal/jobstore, not
// here.
type Store interface {
	// Dir returns the absolute result directory for jobID.
	Dir(jobID string) string
	// Ensure creates the result directory (and parents) for jobID. It returns an
	// error if the directory already exists, so the job service can detect an id
	// collision and retry with a new id.
	Ensure(jobID string) error
	// LogWriter opens (truncating) the stdout/stderr log file for streaming. The
	// caller owns closing the returned writer.
	LogWriter(jobID string, stream Stream) (io.WriteCloser, error)
	// ReadLogTail returns the last maxBytes of a log stream (whole file when
	// maxBytes <= 0). It is the read path used by the HTTP log endpoints (P5).
	ReadLogTail(jobID string, stream Stream, maxBytes int64) ([]byte, error)
}
