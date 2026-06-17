// Package store persists per-job artifacts under a single result directory
// (<result_dir>/<job_id>/): the original request, the live stdout/stderr logs
// and the final result. See plan §9 (P4) and §10.1 (store must-test points).
//
// Directory layout for one job:
//
//	<result_dir>/<job_id>/
//	  request.json        // JobRequest, written at creation
//	  stdout.log          // child process stdout, streamed by the local runner
//	  stderr.log          // child process stderr, streamed by the local runner
//	  result.json         // final JobResult, written atomically (tmp + rename)
//	  interactions.jsonl  // reserved for P9 (running-agent interactions); P4
//	                      // only fixes the filename/location, never writes it.
package store

import (
	"encoding/json"
	"io"
)

// Artifact file names inside a job result directory.
const (
	RequestFile = "request.json"
	StdoutFile  = "stdout.log"
	StderrFile  = "stderr.log"
	ResultFile  = "result.json"
	// InteractionsFile is reserved for P9. P4 only defines the constant and the
	// directory placement (same job dir); nothing writes it yet.
	InteractionsFile = "interactions.jsonl"
	// IndexFile is the per-project append-only job index, one JSON line per
	// JobResult snapshot (create + terminal), used by the list endpoint (web-T2).
	IndexFile = "jobs.jsonl"
)

// Stream identifies which log file to open/read.
type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

// Store abstracts per-job artifact persistence. FileStore is the only
// implementation in the MVP; the interface lets the job service and tests
// depend on behaviour rather than the filesystem layout.
type Store interface {
	// Dir returns the absolute result directory for jobID.
	Dir(jobID string) string
	// Ensure creates the result directory (and parents) for jobID. It returns an
	// error if the directory already exists, so the job service can detect an id
	// collision and retry with a new id.
	Ensure(jobID string) error
	// WriteRequest persists the request payload as request.json.
	WriteRequest(jobID string, req any) error
	// LogWriter opens (truncating) the stdout/stderr log file for streaming. The
	// caller owns closing the returned writer.
	LogWriter(jobID string, stream Stream) (io.WriteCloser, error)
	// WriteResult atomically writes result.json (tmp file + rename).
	WriteResult(jobID string, result any) error
	// ReadResult decodes result.json into out.
	ReadResult(jobID string, out any) error
	// ReadLogTail returns the last maxBytes of a log stream (whole file when
	// maxBytes <= 0). It is the read path used by the HTTP log endpoints (P5).
	ReadLogTail(jobID string, stream Stream, maxBytes int64) ([]byte, error)
	// AppendIndex appends one JSON line (a JobResult snapshot) to
	// <base>/jobs.jsonl. Callers serialise concurrent appends.
	AppendIndex(rec any) error
	// ReadIndex reads every line of the index. A missing file yields an empty
	// slice and no error; corrupt lines are skipped (best-effort tolerance).
	ReadIndex() ([]json.RawMessage, error)
}
