package job

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
	"github.com/inhere/gofer/internal/store"
)

// ndjsonCapture wraps a job's stdout writer in an ndjsonfilter.Filter plus, when
// the agent asked for it, the verbatim stdout.raw.log sidecar. It owns the stdout
// writer it wraps: closing the capture closes the whole chain.
type ndjsonCapture struct {
	filter *ndjsonfilter.Filter
	under  io.WriteCloser
	raw    *os.File
}

// Write implements io.Writer (the runner streams the child's stdout into it).
func (c *ndjsonCapture) Write(p []byte) (int, error) { return c.filter.Write(p) }

// Close flushes the filter's final line, then closes the raw sidecar and the
// underlying stdout.log writer. A second Close is harmless (ignored errors).
func (c *ndjsonCapture) Close() error {
	err := c.filter.Close()
	if c.raw != nil {
		if cerr := c.raw.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	if cerr := c.under.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return err
}

// captureStdoutNDJSON wraps w in the capture-time NDJSON filter when the job's
// agent captures structured output (bd h-aii-rpky: `omp --mode json` /
// `claude --output-format stream-json` emit 10-30x log volume in per-token
// incremental events). w is returned UNCHANGED for a text agent, for a remote
// runner (the executing machine already filtered and mirrors the compact stream
// back) and for an unknown agent.
//
// The filter keeps the agent's whitelisted events (built-in defaults by agent name
// when ndjson_keep is unset) and always the `session` row that session capture
// reads out of stdout.log; every other line is passed through verbatim.
func (s *Service) captureStdoutNDJSON(entry *jobEntry, jobID, runnerName string, w io.WriteCloser) io.WriteCloser {
	if runnerName != builtinLocalRunner {
		return w
	}
	agentKey, resultDir := entryAgentAndDir(entry)
	ac, ok := s.agents.Get(agentKey)
	if !ok || !ac.NDJSONOutput() {
		return w
	}

	opt := ndjsonfilter.Options{Keep: ac.NDJSONKeep}
	var raw *os.File
	if ac.NDJSONRaw && resultDir != "" {
		f, err := os.OpenFile(filepath.Join(resultDir, store.StdoutRawFile), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			// best-effort 排障旁路：打不开就不写 raw，绝不影响 job。
			slog.Warn("job.ndjson_raw_sidecar", "job_id", jobID, "agent", agentKey, "err", err)
		} else {
			raw = f
			opt.Raw = f
		}
	}
	return &ndjsonCapture{filter: ndjsonfilter.New(w, opt), under: w, raw: raw}
}

// recordNDJSONCounts writes the filter's kept/dropped line counts onto the job
// result before finish() persists it, and logs them once per job. A no-op for a
// writer that is not an ndjson capture (text agent / remote runner).
func (s *Service) recordNDJSONCounts(entry *jobEntry, jobID string, w io.WriteCloser) {
	c, ok := w.(*ndjsonCapture)
	if !ok {
		return
	}
	kept, dropped := c.filter.Counts()
	entry.mu.Lock()
	entry.result.NDJSONKept = kept
	entry.result.NDJSONDropped = dropped
	agentKey := entry.result.Agent
	entry.mu.Unlock()
	slog.Info("job.ndjson_capture", "job_id", jobID, "agent", agentKey, "kept", kept, "dropped", dropped)
}

// entryAgentAndDir snapshots the agent key and result dir of a job entry.
func entryAgentAndDir(entry *jobEntry) (agentKey, resultDir string) {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.result.Agent, entry.result.ResultDir
}
