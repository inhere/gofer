package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/store"
)

const (
	acpArtifactFile   = "acp.jsonl"
	maxToolInputRunes = 2000
)

// ACPStreamOpts carries request parameters already validated by the HTTP layer.
type ACPStreamOpts struct {
	TailEvents int
}

// StreamACP normalizes one job's ACP JSONL artifact into connection-local ordered
// events. Historical jobs replay once; a live job follows file growth until its job
// status is finished. HTTP owns authentication, headers and request parsing.
func StreamACP(ctx context.Context, w io.Writer, flusher http.Flusher, jobs *job.Service, id string, res job.JobResult, live bool, opts ACPStreamOpts) {
	artifactPath := filepath.Join(res.ResultDir, "artifacts", acpArtifactFile)
	stdoutPath := filepath.Join(res.ResultDir, store.StdoutFile)
	terminal := !live || job.IsFinished(res.Status)

	reader := acpLineReader{}
	normalizer := acpNormalizer{}
	chunk, offset, _ := TailFrom(artifactPath, 0)
	initial := normalizeACPLines(&reader, &normalizer, chunk, terminal)
	if terminal {
		initial = append(initial, normalizer.finish(readACPStdout(stdoutPath))...)
	} else {
		initial = append(initial, normalizer.snapshot()...)
	}

	seq := 0
	emit := func(event map[string]any) bool {
		seq++
		event["seq"] = seq
		event = capACPEvent(event)
		return writeSSE(w, flusher, "acp", event) == nil
	}
	if opts.TailEvents > 0 && len(initial) > opts.TailEvents {
		skipped := len(initial) - opts.TailEvents
		if !emit(map[string]any{"kind": "truncated", "skipped": skipped}) {
			return
		}
		initial = initial[skipped:]
	}
	for _, event := range initial {
		if !emit(event) {
			return
		}
	}
	if terminal {
		_ = writeSSE(w, flusher, "end", struct{}{})
		return
	}

	ticker := time.NewTicker(StreamPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			more, next, rotated := TailFrom(artifactPath, offset)
			if rotated {
				offset = 0
				reader.partial = nil
				more, next, _ = TailFrom(artifactPath, 0)
			}
			offset = next
			for _, event := range normalizeACPLines(&reader, &normalizer, more, false) {
				if !emit(event) {
					return
				}
			}

			current := res
			if jobs != nil {
				if latest, ok := jobs.Get(id); ok {
					current = latest
				}
			}
			if !job.IsFinished(current.Status) {
				continue
			}
			// Drain bytes appended between the poll read and the terminal snapshot,
			// then finish a final partial line, thought and deferred stop record.
			more, next, _ = TailFrom(artifactPath, offset)
			offset = next
			for _, event := range normalizeACPLines(&reader, &normalizer, more, true) {
				if !emit(event) {
					return
				}
			}
			for _, event := range normalizer.finish(readACPStdout(stdoutPath)) {
				if !emit(event) {
					return
				}
			}
			_ = writeSSE(w, flusher, "end", struct{}{})
			return
		}
	}
}

type acpLineReader struct {
	partial []byte
}

func (r *acpLineReader) feed(chunk []byte, final bool) [][]byte {
	data := append(r.partial, chunk...)
	r.partial = nil
	var lines [][]byte
	for {
		index := bytes.IndexByte(data, '\n')
		if index < 0 {
			break
		}
		line := bytes.TrimSuffix(data[:index], []byte{'\r'})
		if len(bytes.TrimSpace(line)) > 0 {
			lines = append(lines, append([]byte(nil), line...))
		}
		data = data[index+1:]
	}
	if final {
		if line := bytes.TrimSpace(data); len(line) > 0 {
			lines = append(lines, append([]byte(nil), line...))
		}
	} else if len(data) > 0 {
		r.partial = append(r.partial, data...)
	}
	return lines
}

func normalizeACPLines(reader *acpLineReader, normalizer *acpNormalizer, chunk []byte, final bool) []map[string]any {
	var events []map[string]any
	for _, line := range reader.feed(chunk, final) {
		var record map[string]any
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		events = append(events, normalizer.record(record)...)
	}
	return events
}

type acpNormalizer struct {
	thought          strings.Builder
	thoughtTruncated bool
	sawMessage       bool
	pendingStop      map[string]any
}

func (n *acpNormalizer) record(record map[string]any) []map[string]any {
	kind, _ := record["t"].(string)
	switch kind {
	case "thought":
		// DEPRECATED(v0.65): remove in v0.68 after pre-v0.65 token-per-line
		// thought artifacts have aged out of retention.
		if text, ok := record["text"].(string); ok {
			n.thought.WriteString(text)
		}
		n.thoughtTruncated = n.thoughtTruncated || truthy(record["truncated"])
		return nil
	case "session", "available_commands_update", "session_info_update", "mode", "current_mode_update", "set_mode":
		return nil
	}

	events := n.flushThought()
	switch kind {
	case "prompt", "message":
		event := textEvent(kind, record)
		if kind == "message" {
			n.sawMessage = true
		}
		return append(events, event)
	case "tool_call", "tool_call_update":
		return append(events, toolEvent(record))
	case "permission":
		return append(events, permissionEvent(record))
	case "plan":
		return append(events, selectEvent("plan", record, "entries"))
	case "usage_update", "usage":
		return append(events, usageEvent(record))
	case "stop":
		n.pendingStop = selectEvent("stop", record, "stop_reason")
		return events
	default:
		return events
	}
}

func (n *acpNormalizer) snapshot() []map[string]any {
	return n.flushThought()
}

func (n *acpNormalizer) finish(stdout string) []map[string]any {
	events := n.flushThought()
	if !n.sawMessage && stdout != "" {
		// DEPRECATED(v0.65): remove in v0.68 after message-less pre-v0.65 ACP
		// artifacts have aged out of retention.
		events = append(events, map[string]any{"kind": "message", "text": stdout})
	}
	if n.pendingStop != nil {
		events = append(events, n.pendingStop)
		n.pendingStop = nil
	}
	return events
}

func (n *acpNormalizer) flushThought() []map[string]any {
	if n.thought.Len() == 0 {
		return nil
	}
	event := map[string]any{"kind": "thought", "text": n.thought.String()}
	if n.thoughtTruncated {
		event["truncated"] = true
	}
	n.thought.Reset()
	n.thoughtTruncated = false
	return []map[string]any{event}
}

func textEvent(kind string, record map[string]any) map[string]any {
	event := map[string]any{"kind": kind, "text": stringValue(record["text"])}
	if truthy(record["truncated"]) {
		event["truncated"] = true
	}
	return event
}

func toolEvent(record map[string]any) map[string]any {
	event := selectEvent("tool", record, "tool_call_id", "title", "status", "locations", "raw_output")
	if toolKind, ok := record["kind"].(string); ok && toolKind != "" {
		event["tool_kind"] = toolKind
	}
	if raw, ok := record["raw_input"]; ok {
		event["raw_input"] = truncateRunes(stringValue(raw), maxToolInputRunes)
	}
	return event
}

func permissionEvent(record map[string]any) map[string]any {
	event := selectEvent("permission", record,
		"tool_call_id", "title", "mode", "options", "outcome", "auto", "reason", "option_id", "option_kind", "raw_input")
	if toolKind, ok := record["kind"].(string); ok && toolKind != "" {
		event["tool_kind"] = toolKind
	}
	return event
}

func usageEvent(record map[string]any) map[string]any {
	event := map[string]any{"kind": "usage"}
	for key, value := range record {
		if key != "t" {
			event[key] = value
		}
	}
	return event
}

func selectEvent(kind string, record map[string]any, fields ...string) map[string]any {
	event := map[string]any{"kind": kind}
	for _, field := range fields {
		if value, ok := record[field]; ok {
			event[field] = value
		}
	}
	return event
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case float64:
		return typed != 0
	default:
		return false
	}
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	b, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(b)
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func capACPEvent(event map[string]any) map[string]any {
	if MaxSSEFrameBytes <= 0 || encodedLen(event) <= MaxSSEFrameBytes {
		return event
	}
	event["truncated"] = true
	for _, key := range []string{"text", "raw_input", "raw_output", "raw"} {
		text, ok := event[key].(string)
		if !ok || text == "" {
			continue
		}
		runes := []rune(text)
		low, high := 0, len(runes)
		for low < high {
			mid := (low + high + 1) / 2
			event[key] = string(runes[:mid])
			if encodedLen(event) <= MaxSSEFrameBytes {
				low = mid
			} else {
				high = mid - 1
			}
		}
		event[key] = string(runes[:low])
		if encodedLen(event) <= MaxSSEFrameBytes {
			return event
		}
	}
	// A pathological non-text record must still respect the frame ceiling.
	return map[string]any{"seq": event["seq"], "kind": event["kind"], "truncated": true}
}

func encodedLen(event map[string]any) int {
	b, err := json.Marshal(event)
	if err != nil {
		return 0
	}
	return len(b)
}

func readACPStdout(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}
