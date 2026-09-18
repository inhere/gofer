package job

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
	"github.com/inhere/gofer/internal/store"
)

// ndjsonCapture wraps a job's stdout writer in an ndjsonfilter.Filter plus, when
// the agent asked for it, the verbatim stdout.raw.log sidecar. It owns the stdout
// writer it wraps: closing the capture closes the whole chain. The event stream
// (stderr.log by default) is written through the filter but stays owned by the
// caller.
type ndjsonCapture struct {
	filter *ndjsonfilter.Filter
	under  io.WriteCloser
	raw    *os.File
}

// Write implements io.Writer (the runner streams the child's stdout into it).
func (c *ndjsonCapture) Write(p []byte) (int, error) { return c.filter.Write(p) }

// Close flushes the filter's final line and final answer, then closes the raw
// sidecar and the underlying stdout.log writer. A second Close is harmless
// (ignored errors).
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

// captureNDJSON wraps w in the capture-time NDJSON projector when the job's agent
// captures structured output (bd h-aii-rpky: `omp --mode json` /
// `claude --output-format stream-json` emit 10-30x log volume in per-token
// incremental events). The projected result (bd h-aii-525u) is split: stdout.log
// gets the agent's final answer, stderr.log the compact event lines. w is
// returned UNCHANGED for a text agent, for a remote runner (the executing machine
// already projected and mirrors the two streams back) and for an unknown agent.
//
// The whitelist (built-in per agent name when ndjson_keep is unset) gates what
// reaches the projector; the `session` row is always read, and the projector's
// session id is reported for session capture / resume.
func (s *Service) captureNDJSON(entry *jobEntry, jobID, runnerName string, stdout, stderr io.WriteCloser) io.WriteCloser {
	if runnerName != builtinLocalRunner {
		return stdout
	}
	agentKey, resultDir := entryAgentAndDir(entry)
	ac, ok := s.agents.Get(agentKey)
	if !ok || !ac.NDJSONOutput() {
		return stdout
	}

	opt := ndjsonfilter.Options{
		Keep:             ac.NDJSONKeep,
		Projector:        agent.NDJSONProjectorFor(agentKey, ac),
		EventsToStdout:   ac.NDJSONEventsTo == config.NDJSONEventsStdout,
		StdoutEvents:     ac.NDJSONStdout == config.NDJSONStdoutEvents,
		AllAssistantText: ac.NDJSONStdout == config.NDJSONStdoutAssistantText,
		StdoutPath:       ac.NDJSONStdoutPath,
		Fields:           ac.NDJSONFields,
	}
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
	return &ndjsonCapture{filter: ndjsonfilter.New(stdout, stderr, opt), under: stdout, raw: raw}
}

// recordNDJSONCapture writes what the projector observed onto the job result
// before finish() persists it, and logs it once per job: the kept/dropped/
// truncated line counts (audit) and the session id the stream carried (taking
// precedence over the regex capture that scans the log files). A no-op for a
// writer that is not an ndjson capture (text agent / remote runner).
func (s *Service) recordNDJSONCapture(entry *jobEntry, jobID string, w io.WriteCloser) {
	c, ok := w.(*ndjsonCapture)
	if !ok {
		return
	}
	kept, dropped := c.filter.Counts()
	truncated := c.filter.Truncated()
	sessionID := c.filter.SessionID()
	usage := c.filter.Usage()
	entry.mu.Lock()
	entry.result.NDJSONKept = kept
	entry.result.NDJSONDropped = dropped
	entry.result.NDJSONTruncated = truncated
	if sessionID != "" && entry.result.SessionID == "" {
		entry.result.SessionID = sessionID
	}
	// SUP-01 E: the projector knows which row of the agent's stream carries the run's
	// final token/cost tally, so the usage it kept is the job's own accounting. The
	// value is copied: the filter's copy stays the filter's.
	if usage != nil {
		u := *usage
		entry.result.Usage = &u
	}
	agentKey := entry.result.Agent
	entry.mu.Unlock()
	slog.Info("job.ndjson_capture", "job_id", jobID, "agent", agentKey,
		"kept", kept, "dropped", dropped, "truncated", truncated, "session_id", sessionID)
}

// entryAgentAndDir snapshots the agent key and result dir of a job entry.
func entryAgentAndDir(entry *jobEntry) (agentKey, resultDir string) {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.result.Agent, entry.result.ResultDir
}
