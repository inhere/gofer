package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gookit/rux/v2"

	"dev-agent-bridge/internal/job"
	"dev-agent-bridge/internal/store"
)

// streamPollInterval is how often the SSE loop polls the log files and the job
// status for changes (web-T3). Logs are read incrementally from a byte offset.
const streamPollInterval = 250 * time.Millisecond

// logFrame is the JSON payload of a `log` SSE event: which stream the bytes came
// from, a monotonic sequence number and the newly appended text.
type logFrame struct {
	Stream string `json:"stream"`
	Seq    int    `json:"seq"`
	Text   string `json:"text"`
}

// interactionFrame is the JSON payload of an `interaction` SSE event (web-P2 W1):
// the action derived from the interaction's current status (open/answered/
// cancelled) plus the full interaction snapshot.
type interactionFrame struct {
	Action      string          `json:"action"`
	Interaction job.Interaction `json:"interaction"`
}

// handleJobStream serves Server-Sent Events for a single job: incremental
// stdout/stderr (`log` events) plus `status` events on every status change, and
// a final `end` event once the job reaches a terminal state (web-T3).
//
// The endpoint works for both live jobs (in-memory, status polled each tick) and
// historical jobs surviving a restart (resolved via ListJobs, status is then
// static so we replay logs and close immediately).
func (s *Server) handleJobStream(c *rux.Context) {
	id := c.Param("id")

	res, live := s.jobs.Get(id)
	if !live {
		// Fall back to the cross-project index so a job that survived a restart
		// (status no longer tracked in-memory) can still be streamed/replayed.
		jobs, _ := s.jobs.ListJobs(job.ListOpts{})
		for _, jr := range jobs {
			if jr.ID == id {
				res = jr
				break
			}
		}
		if res.ID == "" {
			writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
			return
		}
	}

	// SSE needs a flushable writer; bail before sending any body if the
	// environment cannot stream (e.g. httptest.NewRecorder).
	flusher, ok := c.Resp.(http.Flusher)
	if !ok {
		writeError(c, http.StatusInternalServerError, "streaming unsupported", "response writer is not a flusher")
		return
	}

	w := c.Resp
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	// Open the stream with an SSE comment line. Writing (rather than only
	// flushing) commits the status code now, so the rux response wrapper does not
	// re-emit it at end-of-chain ("superfluous WriteHeader").
	if _, err := io.WriteString(w, ": open\n\n"); err != nil {
		return
	}
	flusher.Flush()

	base := filepath.Dir(res.ResultDir)
	stdoutPath := filepath.Join(base, id, store.StdoutFile)
	stderrPath := filepath.Join(base, id, store.StderrFile)

	// stdout supports resume via ?from (a byte offset); a missing/negative/invalid
	// value starts from the beginning. stderr always starts at 0.
	var stdoutOff int64
	if from, err := strconv.ParseInt(c.Query("from"), 10, 64); err == nil && from > 0 {
		stdoutOff = from
	}
	var stderrOff int64
	seq := 0

	// pumpLogs reads the new bytes appended to each stream since the last offset
	// and emits a `log` event per stream that grew. Offsets/seq are updated in
	// place. A write error (client gone) aborts by returning it.
	pumpLogs := func() error {
		for _, ent := range []struct {
			stream string
			path   string
			off    *int64
		}{
			{string(store.StreamStdout), stdoutPath, &stdoutOff},
			{string(store.StreamStderr), stderrPath, &stderrOff},
		} {
			chunk, next := tailFrom(ent.path, *ent.off)
			if len(chunk) == 0 {
				continue
			}
			*ent.off = next
			seq++
			if err := writeSSE(w, flusher, "log", logFrame{Stream: ent.stream, Seq: seq, Text: string(chunk)}); err != nil {
				return err
			}
		}
		return nil
	}

	// seenStatus tracks the last status we emitted per interaction id, so we only
	// send an `interaction` event when one is raised or changes state.
	seenStatus := map[string]string{}

	// pumpInteractions emits an `interaction` event for every interaction whose
	// status differs from the last one we sent. The action is derived from the
	// status (pending→open, answered→answered, cancelled→cancelled); unknown
	// statuses are skipped. A write error (client gone) aborts by returning it.
	pumpInteractions := func() error {
		its, _ := s.jobs.GetInteractions(id)
		for _, it := range its {
			if seenStatus[it.ID] == it.Status {
				continue
			}
			var action string
			switch it.Status {
			case job.InteractionPending:
				action = "open"
			case job.InteractionAnswered:
				action = "answered"
			case job.InteractionCancelled:
				action = "cancelled"
			default:
				continue
			}
			if err := writeSSE(w, flusher, "interaction", interactionFrame{Action: action, Interaction: it}); err != nil {
				return err
			}
			seenStatus[it.ID] = it.Status
		}
		return nil
	}

	// Initial status snapshot.
	if err := writeSSE(w, flusher, "status", res); err != nil {
		return
	}

	// Replay the current interaction state to a freshly-connected client (pending
	// ones surface as open, already-answered ones as answered).
	if err := pumpInteractions(); err != nil {
		return
	}

	// curStatus tracks the last status we emitted so we only send a `status`
	// event on an actual change.
	curStatus := res.Status

	// finish replays any remaining log bytes, emits a terminal `status` and the
	// closing `end` event.
	finish := func(final job.JobResult) {
		_ = pumpLogs()
		_ = pumpInteractions() // push the last answer/cancel before closing
		_ = writeSSE(w, flusher, "status", final)
		_ = writeSSE(w, flusher, "end", struct{}{})
	}

	// Historical (non-live) jobs are already at a static status: replay the logs
	// once and close. If the in-memory job is already terminal we likewise finish
	// immediately without waiting for a tick.
	if !live || job.IsTerminal(res.Status) {
		finish(res)
		return
	}

	ticker := time.NewTicker(streamPollInterval)
	defer ticker.Stop()

	ctx := c.Req.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := pumpLogs(); err != nil {
				return // client disconnected
			}
			if err := pumpInteractions(); err != nil {
				return // client disconnected
			}

			cur, ok := s.jobs.Get(id)
			if !ok {
				cur = res // job evicted from memory; fall back to the last snapshot
			}
			if cur.Status != curStatus {
				curStatus = cur.Status
				if err := writeSSE(w, flusher, "status", cur); err != nil {
					return
				}
			}
			if job.IsTerminal(cur.Status) {
				finish(cur)
				return
			}
		}
	}
}

// tailFrom reads the bytes of path starting at byte offset, returning the new
// chunk and the next offset (offset+len(chunk)). A missing file (job not yet
// started, or stream never produced) yields an empty chunk and the unchanged
// offset, so callers can keep polling without erroring.
func tailFrom(path string, offset int64) ([]byte, int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset
	}
	defer f.Close()

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset
	}
	data, err := io.ReadAll(f)
	if err != nil || len(data) == 0 {
		return nil, offset
	}
	return data, offset + int64(len(data))
}

// writeSSE encodes data as JSON and writes one SSE frame
// (`event: <event>\ndata: <json>\n\n`), then flushes. Encoding the data object
// with json.Marshal keeps embedded newlines/quotes from corrupting the frame.
func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
