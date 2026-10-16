package streaming

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/inhere/gofer/internal/job"
)

// TranscriptTurn is one prompt turn of a job's ACP structured record
// (artifacts/acp.jsonl): the prompt the agent was sent and the assistant text that
// followed it before the next prompt. It is the same projection the workbench
// conversation reads over the ACP stream (gofer-chd9): one normalizer keeps the two
// views from drifting apart.
type TranscriptTurn struct {
	// User is the turn's prompt, empty when the read window began in the middle of a
	// reply (the prompt is behind the cut).
	User string
	// UserTruncated reports that the runner itself cut the prompt (acp.jsonl keeps
	// the first 8000 runes of one prompt).
	UserTruncated bool
	// Reply is the turn's assistant text, message blocks joined by a blank line.
	Reply string
}

// TranscriptWindow is one bounded read of a job's transcript.
type TranscriptWindow struct {
	// Turns are the prompt turns inside the window, in file order.
	Turns []TranscriptTurn
	// Omitted reports that the read did not start at the file's beginning: whatever
	// came before the first turn here was not read, so the window opens
	// mid-conversation.
	Omitted bool
	// Bytes is how much of the file the read consumed, so a caller budgeting its
	// reads over a whole session can subtract it.
	Bytes int64
}

// ReadTranscript reads at most maxBytes of one job's acp.jsonl and projects it into
// prompt turns. maxBytes <= 0 reads the whole file; otherwise the read keeps the
// file's TAIL, because the recent turns are the ones a reader wants, and reports
// Omitted.
//
// A missing file, an unreadable one, or a job that writes no structured record (an
// exec or plain-CLI agent) yields an empty window — never an error: the caller falls
// back to the job's stored request.
func ReadTranscript(res job.JobResult, maxBytes int) TranscriptWindow {
	window := TranscriptWindow{}
	if res.ResultDir == "" {
		return window
	}
	path := filepath.Join(res.ResultDir, "artifacts", acpArtifactFile)
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return window
	}
	size := fi.Size()
	var start int64
	if maxBytes > 0 && size > int64(maxBytes) {
		start = size - int64(maxBytes)
		window.Omitted = true
	}
	chunk, _, _ := TailChunk(path, start, 0)
	window.Bytes = size - start
	if start > 0 {
		// The window begins mid-line, so its first line is a fragment whose record
		// head is behind the cut: drop it instead of handing the parser a partial
		// record. A cut that lands exactly on a line start loses nothing.
		if i := bytes.IndexByte(chunk, '\n'); i >= 0 {
			chunk = chunk[i+1:]
		} else {
			chunk = nil
		}
	}
	if len(chunk) == 0 {
		return window
	}

	reader := acpLineReader{}
	normalizer := acpNormalizer{}
	for _, event := range normalizeACPLines(&reader, &normalizer, chunk, true) {
		switch kind, _ := event["kind"].(string); kind {
		case "prompt":
			window.Turns = append(window.Turns, TranscriptTurn{
				User:          stringValue(event["text"]),
				UserTruncated: truthy(event["truncated"]),
			})
		case "message":
			if len(window.Turns) == 0 {
				// The window opened inside a reply: keep its text on a turn with no
				// prompt rather than dropping what the agent answered.
				window.Turns = append(window.Turns, TranscriptTurn{})
			}
			turn := &window.Turns[len(window.Turns)-1]
			if turn.Reply != "" {
				turn.Reply += "\n\n"
			}
			turn.Reply += stringValue(event["text"])
		}
	}
	return window
}
