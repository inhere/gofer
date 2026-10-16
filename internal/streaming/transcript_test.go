package streaming

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// writeTranscript seeds one job's acp.jsonl with the given records.
func writeTranscript(t *testing.T, res job.JobResult, records ...map[string]any) {
	t.Helper()
	dir := filepath.Join(res.ResultDir, "artifacts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, rec := range records {
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, acpArtifactFile), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReadTranscriptTurns projects a resident session's record: every turn's prompt
// and the assistant text that followed it (gofer-chd9 reads this to show what was
// sent to the agent and what it answered).
func TestReadTranscriptTurns(t *testing.T) {
	t.Parallel()
	res := job.JobResult{ID: "job-1", ResultDir: t.TempDir()}
	writeTranscript(t, res,
		map[string]any{"t": "session", "session_id": "sess-1"},
		map[string]any{"t": "turn_started", "turn": 1},
		map[string]any{"t": "prompt", "text": "first message"},
		map[string]any{"t": "thought", "text": "thinking"},
		map[string]any{"t": "message", "text": "first answer"},
		map[string]any{"t": "message", "text": "second block"},
		map[string]any{"t": "turn_ended", "turn": 1, "stop_reason": "end_turn"},
		map[string]any{"t": "turn_started", "turn": 2},
		map[string]any{"t": "prompt", "text": "say message"},
		map[string]any{"t": "message", "text": "second answer"},
	)

	win := ReadTranscript(res, 0)
	if win.Omitted {
		t.Fatalf("whole-file read reported Omitted")
	}
	if len(win.Turns) != 2 {
		t.Fatalf("turns = %+v, want 2", win.Turns)
	}
	if win.Turns[0].User != "first message" || win.Turns[0].Reply != "first answer\n\nsecond block" {
		t.Fatalf("turn 1 = %+v", win.Turns[0])
	}
	if win.Turns[1].User != "say message" || win.Turns[1].Reply != "second answer" {
		t.Fatalf("turn 2 = %+v", win.Turns[1])
	}
	if win.Bytes == 0 {
		t.Fatalf("Bytes = 0, want the read file size")
	}
}

// TestReadTranscriptEmptyForNonACPJob pins the fallback contract: a job that writes
// no structured record (exec / plain-CLI agent) yields an empty window, not an error,
// so the caller can fall back to the stored request.
func TestReadTranscriptEmptyForNonACPJob(t *testing.T) {
	t.Parallel()
	for _, res := range []job.JobResult{
		{ID: "no-dir"},
		{ID: "missing-file", ResultDir: t.TempDir()},
	} {
		if win := ReadTranscript(res, 4096); len(win.Turns) != 0 || win.Bytes != 0 {
			t.Fatalf("%s: window = %+v, want empty", res.ID, win)
		}
	}
}

// TestReadTranscriptWindowKeepsTail: a bounded read keeps the NEWEST turns and tells
// the caller content was skipped, and it never feeds the parser the partial line the
// cut lands in.
func TestReadTranscriptWindowKeepsTail(t *testing.T) {
	t.Parallel()
	res := job.JobResult{ID: "job-long", ResultDir: t.TempDir()}
	filler := strings.Repeat("x", 300)
	writeTranscript(t, res,
		map[string]any{"t": "prompt", "text": "old prompt " + filler},
		map[string]any{"t": "message", "text": "old answer " + filler},
		map[string]any{"t": "prompt", "text": "new prompt"},
		map[string]any{"t": "message", "text": "new answer"},
	)
	whole, err := os.ReadFile(filepath.Join(res.ResultDir, "artifacts", acpArtifactFile))
	if err != nil {
		t.Fatal(err)
	}

	// Cut one byte BEFORE the newest turn's prompt line: the window then begins inside
	// the previous line, whose record head is behind the cut.
	newTurn := strings.Index(string(whole), `{"t":"prompt","text":"new prompt"}`)
	if newTurn <= 1 {
		t.Fatalf("fixture layout: new turn at %d", newTurn)
	}
	maxBytes := len(whole) - newTurn + 1
	win := ReadTranscript(res, maxBytes)
	if !win.Omitted {
		t.Fatalf("window did not report Omitted: %+v", win)
	}
	if len(win.Turns) != 1 || win.Turns[0].User != "new prompt" || win.Turns[0].Reply != "new answer" {
		t.Fatalf("window turns = %+v, want only the newest turn", win.Turns)
	}
	if win.Bytes != int64(len(whole)-newTurn+1) {
		t.Fatalf("Bytes = %d, want the %d bytes the window covered", win.Bytes, len(whole)-newTurn+1)
	}
}
