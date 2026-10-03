// Package streaming holds the job log/event/interaction SSE streaming
// orchestration. It is a neutral layer between the HTTP handler (which owns
// request parsing, response headers and the flushable writer) and the job
// service (which owns log files, interactions and lifecycle events). It depends
// only on job/store + the standard library and must never import httpapi
// (avoiding a streaming->httpapi import cycle).
package streaming

import (
	"bytes"
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
	MaxSSEFrameBytes = 256 << 10 // 256 KiB

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
	// Off is the byte offset in the log file right after this frame's text. A
	// client that started from ?tail uses it (not a running byte count) as the
	// resume offset (?from / ?stderr_from) when it reconnects.
	Off int64 `json:"off"`
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
	// StdoutFrom is the byte offset to resume stdout from (?from);  A zero/negative value starts from the beginning.
	StdoutFrom int64
	// StderrFrom is the byte offset to resume stderr from (?stderr_from); a
	// zero/negative value starts from the beginning (or ?tail).
	StderrFrom int64
	// TailLines, when > 0, starts each stream at the beginning of its last
	// TailLines lines instead of byte 0 (?tail), so a viewer does not have to
	// replay a multi-megabyte log to see where the job is now. StdoutFrom wins
	// for stdout when both are set.
	TailLines int
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
	// value starts from the beginning.
	var stdoutOff int64
	if opts.StdoutFrom > 0 {
		stdoutOff = opts.StdoutFrom
	}
	var stderrOff int64
	if opts.StderrFrom > 0 {
		stderrOff = opts.StderrFrom
	}
	if opts.TailLines > 0 {
		if opts.StdoutFrom <= 0 {
			stdoutOff = TailLinesOffset(stdoutPath, opts.TailLines)
		}
		if opts.StderrFrom <= 0 {
			stderrOff = TailLinesOffset(stderrPath, opts.TailLines)
		}
	}
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
			// Read at most MaxSSEFrameBytes per iteration so replaying a huge log
			// never materialises the whole file (and never one giant frame); the
			// loop drains until EOF, emitting one contiguous-seq frame per chunk.
			for {
				chunk, next, rotated := TailChunk(ent.path, *ent.off, MaxSSEFrameBytes)
				if rotated {
					// The file shrank under us (rotation/truncation): tell the client to
					// clear this stream's buffer, reset our offset and re-read from 0.
					seq++
					if err := writeSSE(w, flusher, "log-rotated", RotatedFrame{Stream: ent.stream, Seq: seq}); err != nil {
						return volume, err
					}
					*ent.off = 0
					chunk, next, _ = TailChunk(ent.path, 0, MaxSSEFrameBytes)
				}
				if len(chunk) == 0 {
					break
				}
				*ent.off = next
				volume += int64(len(chunk))
				seq++
				if err := writeSSE(w, flusher, "log", LogFrame{Stream: ent.stream, Seq: seq, Text: string(chunk), Off: next}); err != nil {
					return volume, err
				}
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

	// finish replays any remaining log bytes, emits the final `status` and the
	// closing `end` event (IsFinished, so a needs_review job — finished but not
	// terminal — closes its stream too: its process is gone, nothing more is coming).
	finish := func(final job.JobResult) {
		_, _ = pumpLogs()
		_ = pumpInteractions() // push the last answer/cancel before closing
		_ = pumpEvents()       // push the terminal/cancelled events before closing
		_ = writeSSE(w, flusher, "status", final)
		_ = writeSSE(w, flusher, "end", struct{}{})
	}

	// Historical (non-live) jobs are already at a static status: replay the logs
	// once and close. If the in-memory job already FINISHED we likewise finish
	// immediately without waiting for a tick.
	if !live || job.IsFinished(res.Status) {
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
			if job.IsFinished(cur.Status) {
				finish(cur)
				return
			}
		}
	}
}

// tailScanLimit bounds how far back TailLinesOffset looks for line breaks, so
// a log made of a few enormous lines still starts near its end.
const tailScanLimit = 256 << 10

// TailLinesOffset returns the byte offset where the last n lines of path begin
// (a trailing newline does not count as an extra line). It never looks back more
// than tailScanLimit bytes; a missing file or n <= 0 yields 0.
func TailLinesOffset(path string, n int) int64 {
	if n <= 0 {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return 0
	}
	size := info.Size()
	start := size - tailScanLimit
	if start < 0 {
		start = 0
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return 0
	}
	end := len(buf)
	if buf[end-1] == '\n' {
		end--
	}
	for i := end - 1; i >= 0; i-- {
		if buf[i] != '\n' {
			continue
		}
		n--
		if n == 0 {
			return start + int64(i) + 1
		}
	}
	if start == 0 {
		return 0
	}
	// Fewer than n breaks inside the scan window: start at the first full line
	// in it rather than mid-line.
	if i := bytes.IndexByte(buf, '\n'); i >= 0 && i+1 < len(buf) {
		return start + int64(i) + 1
	}
	return start
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
	return TailChunk(path, offset, 0)
}

// TailChunk is TailFrom bounded to at most limit bytes (limit <= 0 means
// unbounded). When the read stops short of EOF the cut is moved back to a UTF-8
// rune boundary, so a multi-byte character is never split across two frames.
func TailChunk(path string, offset int64, limit int) (chunk []byte, next int64, rotated bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, false
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, offset, false
	}
	if offset > 0 && fi.Size() < offset {
		return nil, offset, true
	}
	remain := fi.Size() - offset
	if remain <= 0 {
		return nil, offset, false
	}
	want := remain
	if limit > 0 && want > int64(limit) {
		want = int64(limit)
	}
	buf := make([]byte, want)
	n, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, offset, false
	}
	buf = buf[:n]
	if int64(n) < remain {
		buf = trimPartialRune(buf)
	}
	if len(buf) == 0 {
		return nil, offset, false
	}
	return buf, offset + int64(len(buf)), false
}

// trimPartialRune drops an incomplete trailing UTF-8 sequence (at most 3 bytes).
func trimPartialRune(b []byte) []byte {
	for i := 1; i <= 3 && i <= len(b); i++ {
		c := b[len(b)-i]
		if c&0xC0 == 0x80 {
			continue // continuation byte, keep scanning back
		}
		if c >= 0xC0 {
			need := 2
			if c >= 0xF0 {
				need = 4
			} else if c >= 0xE0 {
				need = 3
			}
			if i < need {
				return b[:len(b)-i]
			}
		}
		return b
	}
	return b
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
