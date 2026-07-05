package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/streaming"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// sseEvent is one parsed SSE frame (event name + raw data line).
type sseEvent struct {
	Event string
	Data  string
}

// createStreamJob POSTs an exec job through the real HTTP server and returns its
// id. It uses the test http.Client against base (an httptest.NewServer URL).
func createStreamJob(t *testing.T, base string, cmd []string) string {
	t.Helper()
	body, err := json.Marshal(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: cmd, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/v1/jobs", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create job status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created job: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("created job has no id")
	}
	return created.ID
}

// openStream opens an authenticated SSE connection for jobID and returns the
// response plus a scanner that yields complete frames (split on the blank line).
// The caller owns closing resp.Body and (optionally) the request ctx cancel.
func openStream(t *testing.T, ctx context.Context, base, jobID, query string) (*http.Response, *bufio.Scanner) {
	t.Helper()
	url := base + "/v1/jobs/" + jobID + "/stream" + query
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("stream status=%d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		resp.Body.Close()
		t.Fatalf("content-type=%q, want text/event-stream", ct)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	// Split on the SSE frame separator: a blank line ("\n\n").
	scanner.Split(scanFrames)
	return resp, scanner
}

// scanFrames is a bufio.SplitFunc that yields one SSE frame per call (everything
// up to and including the terminating blank line).
func scanFrames(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := strings.Index(string(data), "\n\n"); i >= 0 {
		return i + 2, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// parseFrame turns a raw SSE frame ("event: x\ndata: y") into an sseEvent.
func parseFrame(raw string) sseEvent {
	var ev sseEvent
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "event: "):
			ev.Event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev.Data = strings.TrimPrefix(line, "data: ")
		}
	}
	return ev
}

// readFrames consumes frames from scanner until it sees an `end` event or the
// deadline passes, returning every frame collected. The deadline is enforced by
// a watchdog goroutine that closes the body to unblock the blocking Scan.
func readFrames(t *testing.T, resp *http.Response, scanner *bufio.Scanner, timeout time.Duration) []sseEvent {
	t.Helper()
	done := make(chan struct{})
	timer := time.AfterFunc(timeout, func() { resp.Body.Close() })
	defer timer.Stop()
	defer close(done)

	var frames []sseEvent
	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		ev := parseFrame(raw)
		frames = append(frames, ev)
		if ev.Event == "end" {
			break
		}
	}
	return frames
}

// TestStreamRealtimeLog connects to a job that emits three lines with delays and
// asserts log events accumulate line1/line2/line3, then a terminal done status
// and an end event.
func TestStreamRealtimeLog(t *testing.T) {
	s := newTestServer(t, testToken, false)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	id := createStreamJob(t, srv.URL, testcmd.Cmd(t, "stdout-lines", "line", "3", "300ms"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, scanner := openStream(t, ctx, srv.URL, id, "")
	defer resp.Body.Close()

	frames := readFrames(t, resp, scanner, 10*time.Second)

	var logText strings.Builder
	var sawEnd bool
	var lastStatus string
	for _, ev := range frames {
		switch ev.Event {
		case "log":
			var lf streaming.LogFrame
			if err := json.Unmarshal([]byte(ev.Data), &lf); err != nil {
				t.Fatalf("bad log frame %q: %v", ev.Data, err)
			}
			if lf.Stream == "stdout" {
				logText.WriteString(lf.Text)
			}
		case "status":
			var jr job.JobResult
			if err := json.Unmarshal([]byte(ev.Data), &jr); err != nil {
				t.Fatalf("bad status frame %q: %v", ev.Data, err)
			}
			lastStatus = jr.Status
		case "end":
			sawEnd = true
		}
	}

	got := logText.String()
	for _, want := range []string{"line1", "line2", "line3"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stdout log missing %q; got %q", want, got)
		}
	}
	if lastStatus != job.StatusDone {
		t.Fatalf("final status=%q, want done", lastStatus)
	}
	if !sawEnd {
		t.Fatalf("did not receive end event; frames=%v", frames)
	}
}

// TestStreamResumeFrom runs a job to completion, then streams it with ?from set
// to half its stdout length and asserts only the suffix (no prefix) is replayed.
func TestStreamResumeFrom(t *testing.T) {
	s := newTestServer(t, testToken, false)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	id := createStreamJob(t, srv.URL, testcmd.Cmd(t, "printf", "AAAABBBBCCCCDDDD"))

	// Wait for the job to finish, then learn its stdout length.
	final := waitDoneHTTP(t, srv.URL, id)
	if final.Status != job.StatusDone {
		t.Fatalf("setup: status=%q, want done", final.Status)
	}
	full := fetchLog(t, srv.URL, id)
	L := len(full)
	if L < 8 {
		t.Fatalf("setup: stdout too short (%d) to test resume", L)
	}
	half := L / 2

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, scanner := openStream(t, ctx, srv.URL, id, "?from="+strconv.Itoa(half))
	defer resp.Body.Close()

	frames := readFrames(t, resp, scanner, 10*time.Second)

	var logText strings.Builder
	for _, ev := range frames {
		if ev.Event != "log" {
			continue
		}
		var lf streaming.LogFrame
		if err := json.Unmarshal([]byte(ev.Data), &lf); err != nil {
			t.Fatalf("bad log frame: %v", err)
		}
		if lf.Stream == "stdout" {
			logText.WriteString(lf.Text)
		}
	}

	got := logText.String()
	wantSuffix := full[half:]
	if got != wantSuffix {
		t.Fatalf("resume mismatch: got %q, want suffix %q (full %q from=%d)", got, wantSuffix, full, half)
	}
	// The prefix before the offset must not be present.
	if prefix := full[:half]; prefix != "" && strings.Contains(got, prefix) {
		t.Fatalf("resume leaked prefix %q in %q", prefix, got)
	}
}

// TestStreamCompletedJob connects to an already-terminal job and asserts the
// logs are replayed, a terminal status is sent and the stream closes with end.
func TestStreamCompletedJob(t *testing.T) {
	s := newTestServer(t, testToken, false)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	id := createStreamJob(t, srv.URL, testcmd.Cmd(t, "printf", "hello-completed\n"))
	final := waitDoneHTTP(t, srv.URL, id)
	if final.Status != job.StatusDone {
		t.Fatalf("setup: status=%q, want done", final.Status)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, scanner := openStream(t, ctx, srv.URL, id, "")
	defer resp.Body.Close()

	frames := readFrames(t, resp, scanner, 10*time.Second)

	var logText strings.Builder
	var sawEnd bool
	var lastStatus string
	for _, ev := range frames {
		switch ev.Event {
		case "log":
			var lf streaming.LogFrame
			if err := json.Unmarshal([]byte(ev.Data), &lf); err != nil {
				t.Fatalf("bad log frame: %v", err)
			}
			if lf.Stream == "stdout" {
				logText.WriteString(lf.Text)
			}
		case "status":
			var jr job.JobResult
			if err := json.Unmarshal([]byte(ev.Data), &jr); err != nil {
				t.Fatalf("bad status frame: %v", err)
			}
			lastStatus = jr.Status
		case "end":
			sawEnd = true
		}
	}

	if !strings.Contains(logText.String(), "hello-completed") {
		t.Fatalf("replayed log missing output; got %q", logText.String())
	}
	if lastStatus != job.StatusDone {
		t.Fatalf("final status=%q, want done", lastStatus)
	}
	if !sawEnd {
		t.Fatalf("did not receive end event for completed job")
	}
}

// TestStreamClientCancel connects to a long-running job, reads at least one
// frame, cancels the client ctx and asserts the read loop ends within a
// deadline (no goroutine leak / hang). Run with -race.
func TestStreamClientCancel(t *testing.T) {
	s := newTestServer(t, testToken, false)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// A long job: emits lines slowly for well beyond the test horizon.
	id := createStreamJob(t, srv.URL, testcmd.Cmd(t, "stdout-lines", "tick", "100", "200ms"))

	ctx, cancel := context.WithCancel(context.Background())
	resp, scanner := openStream(t, ctx, srv.URL, id, "")
	defer resp.Body.Close()

	// Read until we get at least one log event (proves the stream is live).
	gotEvent := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !gotEvent {
		if !scanner.Scan() {
			break
		}
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		if parseFrame(raw).Event == "log" {
			gotEvent = true
		}
	}
	if !gotEvent {
		cancel()
		t.Fatalf("never received a log event before cancel")
	}

	// Cancel the client: the read loop must terminate within the deadline.
	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for scanner.Scan() {
		}
	}()
	select {
	case <-done:
		// Scan returned (body closed / EOF / error) — read loop ended cleanly.
	case <-time.After(5 * time.Second):
		t.Fatalf("read loop did not end within deadline after client cancel")
	}

	// Cancel the underlying job and wait for it to reach a terminal state so the
	// runner goroutine stops writing into the temp dir before t.TempDir cleanup.
	_ = s.jobs.Cancel(id)
	s.jobs.Wait(id)
}

// TestStreamInteractionEvents connects to a live (long-running) job, then raises
// and answers an interaction directly via the Service while the SSE stream is
// open, asserting an `interaction` event with action=open is delivered when the
// interaction is created and action=answered (carrying the answer) once answered.
func TestStreamInteractionEvents(t *testing.T) {
	s := newTestServer(t, testToken, false)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// A long-running job so the stream stays in the live ticker loop while we
	// drive interactions from the test.
	id := createStreamJob(t, srv.URL, testcmd.Cmd(t, "sleep", "30s"))
	t.Cleanup(func() {
		_ = s.jobs.Cancel(id)
		s.jobs.Wait(id)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, scanner := openStream(t, ctx, srv.URL, id, "")
	defer resp.Body.Close()

	// Drain frames in the background, forwarding `interaction` frames to a channel.
	interactions := make(chan streaming.InteractionFrame, 8)
	go func() {
		for scanner.Scan() {
			raw := strings.TrimSpace(scanner.Text())
			if raw == "" {
				continue
			}
			ev := parseFrame(raw)
			if ev.Event != "interaction" {
				continue
			}
			var f streaming.InteractionFrame
			if err := json.Unmarshal([]byte(ev.Data), &f); err != nil {
				continue
			}
			interactions <- f
		}
	}()

	// Raise an interaction on the live job; the stream must surface it as `open`.
	created, err := s.jobs.CreateInteraction(id, job.InteractionInput{
		Type: job.InteractionTypeQuestion, Prompt: "q",
	})
	if err != nil {
		t.Fatalf("CreateInteraction: %v", err)
	}

	open := awaitInteraction(t, interactions, 5*time.Second)
	if open.Action != "open" {
		t.Fatalf("first interaction action=%q, want open", open.Action)
	}
	if open.Interaction.ID == "" {
		t.Fatalf("open interaction has empty id")
	}
	if open.Interaction.Status != job.InteractionPending {
		t.Fatalf("open interaction status=%q, want pending", open.Interaction.Status)
	}

	// Answer it; the stream must surface the answered state with the answer text.
	if _, err := s.jobs.AnswerInteraction(id, created.ID, "ans"); err != nil {
		t.Fatalf("AnswerInteraction: %v", err)
	}

	answered := awaitInteraction(t, interactions, 5*time.Second)
	if answered.Action != "answered" {
		t.Fatalf("second interaction action=%q, want answered", answered.Action)
	}
	if answered.Interaction.Answer != "ans" {
		t.Fatalf("answered interaction answer=%q, want ans", answered.Interaction.Answer)
	}
}

// awaitInteraction waits for the next interaction frame or fails on timeout.
func awaitInteraction(t *testing.T, ch <-chan streaming.InteractionFrame, timeout time.Duration) streaming.InteractionFrame {
	t.Helper()
	select {
	case f := <-ch:
		return f
	case <-time.After(timeout):
		t.Fatalf("did not receive interaction frame within %s", timeout)
		return streaming.InteractionFrame{}
	}
}

// --- small HTTP helpers (real client, used by the SSE tests) ---

// waitDoneHTTP polls GET /v1/jobs/{id} via the real client until terminal.
func waitDoneHTTP(t *testing.T, base, id string) job.JobResult {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, base+"/v1/jobs/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("poll job: %v", err)
		}
		var jr job.JobResult
		_ = json.NewDecoder(resp.Body).Decode(&jr)
		resp.Body.Close()
		switch jr.Status {
		case job.StatusDone, job.StatusFailed, job.StatusCancelled, job.StatusTimeout:
			return jr
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach terminal state in time", id)
	return job.JobResult{}
}

// fetchLog returns the full stdout log body via the real client.
func fetchLog(t *testing.T, base, id string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/jobs/"+id+"/logs/stdout", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fetch log: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
