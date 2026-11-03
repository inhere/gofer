package job

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/acp/acptest"
)

// lifecycleSink collects the JSON records of the default logger. It is safe for the
// job goroutines that log while the test reads.
type lifecycleSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *lifecycleSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *lifecycleSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// rows returns the records whose event attr is event.
func (s *lifecycleSink) rows(t *testing.T, event string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace([]byte(s.String())), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatalf("invalid log record %q: %v", line, err)
		}
		if row["event"] == event {
			out = append(out, row)
		}
	}
	return out
}

// captureLifecyclePoints swaps the default logger for a sink at DEBUG level (so the
// events this file checks are not filtered) and restores it afterwards.
func captureLifecyclePoints(t *testing.T) *lifecycleSink {
	t.Helper()
	sink := &lifecycleSink{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return sink
}

// TestJobSubmittedAndTerminalLogs drives a real exec job end to end and asserts the
// two lifecycle lines: one job.submitted naming the routing identity, one
// job.terminal naming how it ended — and neither carries the request's prompt.
func TestJobSubmittedAndTerminalLogs(t *testing.T) {
	s := newTestService(t, t.TempDir())
	logs := captureLifecyclePoints(t)
	const secretPrompt = "TOP-SECRET-PROMPT-TEXT"
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		CallerID: "caller-a", Prompt: secretPrompt,
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: status=%s", final.Status)
	}

	submitted := logs.rows(t, "job.submitted")
	if len(submitted) != 1 {
		t.Fatalf("job.submitted rows=%d, want 1: %s", len(submitted), logs.String())
	}
	row := submitted[0]
	for key, want := range map[string]any{
		"job_id": final.ID, "agent": "exec", "runner": "local",
		"project": "self", "caller": "caller-a", "session": false,
	} {
		if row[key] != want {
			t.Errorf("job.submitted %s=%v, want %v (row=%v)", key, row[key], want, row)
		}
	}

	terminal := logs.rows(t, "job.terminal")
	if len(terminal) != 1 {
		t.Fatalf("job.terminal rows=%d, want 1: %s", len(terminal), logs.String())
	}
	term := terminal[0]
	if term["job_id"] != final.ID || term["status"] != StatusDone || term["exit_code"] != float64(0) {
		t.Fatalf("job.terminal row=%v", term)
	}
	if _, ok := term["duration_ms"]; !ok {
		t.Fatalf("job.terminal lost duration_ms: %v", term)
	}
	if _, ok := term["error"]; ok {
		t.Fatalf("a clean job logged an error field: %v", term)
	}
	if bytes.Contains([]byte(logs.String()), []byte(secretPrompt)) {
		t.Fatalf("prompt text leaked into the log: %s", logs.String())
	}
}

// TestResidentSessionLogs drives the resident-session callbacks the ACP runner uses
// (turn end, awaiting input) plus a say, and asserts their log lines: the say names
// the MESSAGE LENGTH only, never its text.
func TestResidentSessionLogs(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{})
	const secretMessage = "TOP-SECRET-SAY-TEXT"
	logs := captureLifecyclePoints(t)

	created := submitSmokeSession(t, s, 20)
	waitSessionTurn(t, s, created.ID, 1)
	if err := s.SaySession(created.ID, secretMessage); err != nil {
		t.Fatalf("say: %v", err)
	}
	waitSessionTurn(t, s, created.ID, 2)
	if err := s.EndSession(created.ID); err != nil {
		t.Fatalf("end session: %v", err)
	}
	if _, ok := s.Wait(created.ID); !ok {
		t.Fatal("session job did not end")
	}

	say := logs.rows(t, "job.say")
	if len(say) != 1 {
		t.Fatalf("job.say rows=%d, want 1: %s", len(say), logs.String())
	}
	if say[0]["job_id"] != created.ID || say[0]["chars"] != float64(utf8.RuneCountInString(secretMessage)) {
		t.Fatalf("job.say row=%v", say[0])
	}
	if bytes.Contains([]byte(logs.String()), []byte(secretMessage)) {
		t.Fatalf("said text leaked into the log: %s", logs.String())
	}

	ended := logs.rows(t, "job.turn_ended")
	if len(ended) == 0 {
		t.Fatalf("no job.turn_ended rows: %s", logs.String())
	}
	if ended[0]["job_id"] != created.ID || ended[0]["turn_no"] == nil || ended[0]["stop_reason"] == nil {
		t.Fatalf("job.turn_ended row=%v", ended[0])
	}

	awaiting := logs.rows(t, "job.awaiting_input")
	if len(awaiting) == 0 {
		t.Fatalf("no job.awaiting_input rows: %s", logs.String())
	}
	if awaiting[0]["job_id"] != created.ID || awaiting[0]["turn_no"] == nil {
		t.Fatalf("job.awaiting_input row=%v", awaiting[0])
	}

	terminal := logs.rows(t, "job.terminal")
	if len(terminal) == 0 || terminal[len(terminal)-1]["job_id"] != created.ID {
		t.Fatalf("the session job's terminal line is missing: %s", logs.String())
	}
}
