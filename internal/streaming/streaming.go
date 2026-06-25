// Package streaming holds the job log/event/interaction SSE streaming
// orchestration. It is a neutral layer between the HTTP handler (which owns
// request parsing, response headers and the flushable writer) and the job
// service (which owns log files, interactions and lifecycle events). It depends
// only on job/store + the standard library and must never import httpapi
// (avoiding a streaming->httpapi import cycle).
package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/store"
)

// StreamPollInterval is how often the SSE loop polls the log files and the job
// status for changes (web-T3). Logs are read incrementally from a byte offset.
const StreamPollInterval = 250 * time.Millisecond

// SSE log-flow-control tunables (C4). All are package vars (not consts) so tests
// can set tiny values without producing megabytes of data.
var (
	// MaxSSEFrameBytes caps the Text payload of a single `log` frame. A larger
	// incremental chunk is split into multiple contiguous-seq frames (no bytes
	// dropped, no truncation) which the frontend reassembles in seq order.
	MaxSSEFrameBytes = 1 << 20 // 1 MiB

	// StreamThrottleBytes is the per-poll new-byte volume above which the loop
	// lengthens the next tick interval to StreamThrottledInterval, spacing out
	// reads under a high-volume producer. It never drops bytes.
	StreamThrottleBytes int64 = 10 << 20 // 10 MiB

	// StreamThrottledInterval is the slower poll cadence used after a high-volume
	// poll; the loop returns to StreamPollInterval once volume calms.
	StreamThrottledInterval = 500 * time.Millisecond
)

// LogFrame is the JSON payload of a `log` SSE event: which stream the bytes came
// from, a monotonic sequence number and the newly appended text.
type LogFrame struct {
	Stream string `json:"stream"`
	Seq    int    `json:"seq"`
	Text   string `json:"text"`
}

// RotatedFrame is the JSON payload of a `log-rotated` SSE event (C4): the
// underlying log file rotated (shrank / our offset now points past EOF), so the
// frontend must clear its buffered text for that stream and continue from the
// fresh file. The read offset is reset to 0 server-side; seq keeps advancing.
type RotatedFrame struct {
	Stream string `json:"stream"`
	Seq    int    `json:"seq"`
}

// InteractionFrame is the JSON payload of an `interaction` SSE event (web-P2 W1):
// the action derived from the interaction's current status (open/answered/
// cancelled) plus the full interaction snapshot.
type InteractionFrame struct {
	Action      string          `json:"action"`
	Interaction job.Interaction `json:"interaction"`
}

// EventFrame is the JSON payload of an `event` SSE event (E13): one append-only
// lifecycle event (job.submitted / job.running / job.terminal / interaction.* /
// …). detail is the raw detail_json string (may be empty); the frontend parses
// it. seq is the cursor the frontend dedups/orders on.
type EventFrame struct {
	Seq    int64  `json:"seq"`
	Type   string `json:"type"`
	Detail string `json:"detail,omitempty"`
	At     int64  `json:"at"`
}

// StreamOpts carries the per-request stream parameters resolved by the HTTP
// handler before delegating the SSE loop to StreamJob.
type StreamOpts struct {
	// StdoutFrom is the byte offset to resume stdout from (?from); stderr always
	// starts at 0. A zero/negative value starts from the beginning.
	StdoutFrom int64
}

// StreamJob serves the Server-Sent Events body for a single job: incremental
// stdout/stderr (`log` events) plus `status` events on every status change, and
// a final `end` event once the job reaches a terminal state (web-T3).
//
// The caller (HTTP handler) owns parsing the request, resolving the job (res +
// live), checking flushability and writing the SSE response headers + opening
// comment; StreamJob then writes SSE frames straight to w (flushing via flusher)
// until the job is terminal or the client disconnects (ctx done / write error).
//
// It works for both live jobs (in-memory, status polled each tick) and
// historical jobs surviving a restart (status static — logs are replayed and the
// stream closed immediately).
func StreamJob(ctx context.Context, w io.Writer, flusher http.Flusher, jobs *job.Service, id string, res job.JobResult, live bool, opts StreamOpts) {
	base := filepath.Dir(res.ResultDir)
	stdoutPath := filepath.Join(base, id, store.StdoutFile)
	stderrPath := filepath.Join(base, id, store.StderrFile)

	// stdout supports resume via ?from (a byte offset); a missing/negative/invalid
	// value starts from the beginning. stderr always starts at 0.
	var stdoutOff int64
	if opts.StdoutFrom > 0 {
		stdoutOff = opts.StdoutFrom
	}
	var stderrOff int64
	seq := 0

	// pumpLogs reads the new bytes appended to each stream since the last offset
	// and emits `log` events for the stream(s) that grew. Offsets/seq are updated
	// in place. It returns the total new-byte volume this poll (used to drive the
	// dynamic throttle) and a write error (client gone) which aborts the loop.
	//
	// Two C4 behaviours layer on the incremental read:
	//   - Frame cap + chunking: a chunk larger than MaxSSEFrameBytes is split into
	//     multiple contiguous-seq `log` frames (no bytes dropped); the frontend
	//     reassembles in seq order.
	//   - Rotation coordination: when the underlying file rotated (shrank below
	//     our offset), emit a `log-rotated` marker, reset the offset to 0 and
	//     re-read the fresh file in the same poll.
	pumpLogs := func() (int64, error) {
		var volume int64
		for _, ent := range []struct {
			stream string
			path   string
			off    *int64
		}{
			{string(store.StreamStdout), stdoutPath, &stdoutOff},
			{string(store.StreamStderr), stderrPath, &stderrOff},
		} {
			chunk, next, rotated := TailFrom(ent.path, *ent.off)
			if rotated {
				// The file shrank under us (rotation/truncation): tell the client to
				// clear this stream's buffer, reset our offset and re-read from 0.
				seq++
				if err := writeSSE(w, flusher, "log-rotated", RotatedFrame{Stream: ent.stream, Seq: seq}); err != nil {
					return volume, err
				}
				*ent.off = 0
				chunk, next, _ = TailFrom(ent.path, 0)
			}
			if len(chunk) == 0 {
				continue
			}
			*ent.off = next
			volume += int64(len(chunk))
			// Split oversize chunks into <=MaxSSEFrameBytes frames with contiguous
			// seq so the frontend can reassemble the exact original bytes in order.
			for len(chunk) > MaxSSEFrameBytes {
				seq++
				if err := writeSSE(w, flusher, "log", LogFrame{Stream: ent.stream, Seq: seq, Text: string(chunk[:MaxSSEFrameBytes])}); err != nil {
					return volume, err
				}
				chunk = chunk[MaxSSEFrameBytes:]
			}
			seq++
			if err := writeSSE(w, flusher, "log", LogFrame{Stream: ent.stream, Seq: seq, Text: string(chunk)}); err != nil {
				return volume, err
			}
		}
		return volume, nil
	}

	// seenStatus tracks the last status we emitted per interaction id, so we only
	// send an `interaction` event when one is raised or changes state.
	seenStatus := map[string]string{}

	// pumpInteractions emits an `interaction` event for every interaction whose
	// status differs from the last one we sent. The action is derived from the
	// status (pending→open, answered→answered, cancelled→cancelled); unknown
	// statuses are skipped. A write error (client gone) aborts by returning it.
	//
	// It reads via GetPersistedInteractions (live in-memory state preferred,
	// interactions.jsonl fallback) using the job's result base, so a terminal job
	// evicted from memory (SP3) still replays its interaction history to a freshly
	// connected client.
	pumpInteractions := func() error {
		its, _ := jobs.GetPersistedInteractions(base, id)
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
			if err := writeSSE(w, flusher, "interaction", InteractionFrame{Action: action, Interaction: it}); err != nil {
				return err
			}
			seenStatus[it.ID] = it.Status
		}
		return nil
	}

	// lastEventSeq is the E13 event cursor: the seq of the last `event` frame we
	// emitted. ListJobEvents(id, lastEventSeq) returns only newer events, so the
	// initial replay (lastEventSeq==0) sends the full history and each subsequent
	// poll sends just the increment — mirroring pumpInteractions' replay+follow.
	var lastEventSeq int64

	// pumpEvents emits an `event` frame for every lifecycle event newer than
	// lastEventSeq and advances the cursor. Events are durable-only (recordEvent
	// writes straight to the DB), so this works for live and evicted jobs alike. A
	// write error (client gone) aborts by returning it.
	pumpEvents := func() error {
		evs, err := jobs.ListJobEvents(id, lastEventSeq)
		if err != nil {
			return nil // best-effort: a read error never aborts the log stream
		}
		for _, ev := range evs {
			if werr := writeSSE(w, flusher, "event", EventFrame{
				Seq: ev.Seq, Type: ev.Type, Detail: ev.Detail, At: ev.At,
			}); werr != nil {
				return werr
			}
			lastEventSeq = ev.Seq
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

	// Replay the current event stream to a freshly-connected client (E13).
	if err := pumpEvents(); err != nil {
		return
	}

	// curStatus tracks the last status we emitted so we only send a `status`
	// event on an actual change.
	curStatus := res.Status

	// finish replays any remaining log bytes, emits a terminal `status` and the
	// closing `end` event.
	finish := func(final job.JobResult) {
		_, _ = pumpLogs()
		_ = pumpInteractions() // push the last answer/cancel before closing
		_ = pumpEvents()       // push the terminal/cancelled events before closing
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

	ticker := time.NewTicker(StreamPollInterval)
	defer ticker.Stop()
	// throttled tracks whether the loop is currently on the slower cadence, so we
	// only Reset the ticker on a transition (avoids resetting every tick).
	throttled := false

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			volume, err := pumpLogs()
			if err != nil {
				return // client disconnected
			}
			// Dynamic throttle: a high-volume poll lengthens the next interval to
			// space out reads; once volume calms, return to the normal cadence.
			// Throttling only spaces out reads — no bytes are dropped.
			if volume > StreamThrottleBytes && !throttled {
				throttled = true
				ticker.Reset(StreamThrottledInterval)
			} else if volume <= StreamThrottleBytes && throttled {
				throttled = false
				ticker.Reset(StreamPollInterval)
			}
			if err := pumpInteractions(); err != nil {
				return // client disconnected
			}
			if err := pumpEvents(); err != nil {
				return // client disconnected
			}

			cur, ok := jobs.Get(id)
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

// TailFrom reads the bytes of path starting at byte offset, returning the new
// chunk, the next offset (offset+len(chunk)) and a rotated flag. A missing file
// (job not yet started, or stream never produced) yields an empty chunk and the
// unchanged offset, so callers can keep polling without erroring.
//
// rotated is true when the file is now smaller than offset — i.e. the live log
// was rotated/truncated under us (C4). In that case the caller should emit a
// rotation marker and re-read from offset 0; the returned chunk is empty.
func TailFrom(path string, offset int64) (chunk []byte, next int64, rotated bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, false
	}
	defer f.Close()

	if offset > 0 {
		if fi, err := f.Stat(); err == nil && fi.Size() < offset {
			return nil, offset, true
		}
	}

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, false
	}
	data, err := io.ReadAll(f)
	if err != nil || len(data) == 0 {
		return nil, offset, false
	}
	return data, offset + int64(len(data)), false
}

// writeSSE encodes data as JSON and writes one SSE frame
// (`event: <event>\ndata: <json>\n\n`), then flushes. Encoding the data object
// with json.Marshal keeps embedded newlines/quotes from corrupting the frame.
func writeSSE(w io.Writer, flusher http.Flusher, event string, data any) error {
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
