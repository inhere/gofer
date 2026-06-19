package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/store"
)

// setSSEFrameCap temporarily lowers maxSSEFrameBytes for the duration of a test.
func setSSEFrameCap(t *testing.T, cap int) {
	t.Helper()
	prev := maxSSEFrameBytes
	maxSSEFrameBytes = cap
	t.Cleanup(func() { maxSSEFrameBytes = prev })
}

// setThrottleBytes temporarily lowers streamThrottleBytes for the duration of a test.
func setThrottleBytes(t *testing.T, n int64) {
	t.Helper()
	prev := streamThrottleBytes
	streamThrottleBytes = n
	t.Cleanup(func() { streamThrottleBytes = prev })
}

// collectStdout reassembles every stdout `log` frame in seq order and returns the
// concatenated text plus the ordered list of seq numbers seen (across all frames).
func collectStdout(frames []sseEvent) (text string, stdoutSeqs []int, allSeqs []int) {
	var b strings.Builder
	for _, ev := range frames {
		switch ev.Event {
		case "log":
			var lf logFrame
			if json.Unmarshal([]byte(ev.Data), &lf) != nil {
				continue
			}
			allSeqs = append(allSeqs, lf.Seq)
			if lf.Stream == "stdout" {
				b.WriteString(lf.Text)
				stdoutSeqs = append(stdoutSeqs, lf.Seq)
			}
		case "log-rotated":
			var rf rotatedFrame
			if json.Unmarshal([]byte(ev.Data), &rf) == nil {
				allSeqs = append(allSeqs, rf.Seq)
			}
		}
	}
	return b.String(), stdoutSeqs, allSeqs
}

// TestStreamFrameCapSplitsChunk drives a completed job whose stdout exceeds a
// tiny frame cap and asserts the replay is split into multiple `log` frames with
// monotonically increasing seq, and that concatenating them in arrival order
// restores the exact original stdout (no bytes dropped, no truncation).
func TestStreamFrameCapSplitsChunk(t *testing.T) {
	setSSEFrameCap(t, 64) // 64-byte frames

	s := newTestServer(t, testToken, false)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// Emit ~500 bytes of deterministic stdout in one shot.
	id := createStreamJob(t, srv.URL, []string{"sh", "-c", "for i in $(seq 1 50); do printf 'LINE%02d-XXXX\\n' $i; done"})
	final := waitDoneHTTP(t, srv.URL, id)
	if final.Status != job.StatusDone {
		t.Fatalf("setup: status=%q, want done", final.Status)
	}
	want := fetchLog(t, srv.URL, id)
	if len(want) <= 64 {
		t.Fatalf("setup: stdout too short (%d) to exercise frame cap", len(want))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, scanner := openStream(t, ctx, srv.URL, id, "")
	defer resp.Body.Close()

	frames := readFrames(t, resp, scanner, 10*time.Second)
	got, stdoutSeqs, _ := collectStdout(frames)

	if got != want {
		t.Fatalf("reassembled stdout mismatch:\n got %q\nwant %q", got, want)
	}
	// More than one stdout frame must have been produced (the cap forced splits).
	if len(stdoutSeqs) < 2 {
		t.Fatalf("expected >=2 stdout frames under a tiny cap, got %d", len(stdoutSeqs))
	}
	// No individual frame exceeds the cap.
	for _, ev := range frames {
		if ev.Event != "log" {
			continue
		}
		var lf logFrame
		_ = json.Unmarshal([]byte(ev.Data), &lf)
		if len(lf.Text) > 64 {
			t.Fatalf("frame seq=%d exceeds cap: %d bytes", lf.Seq, len(lf.Text))
		}
	}
	// stdout seqs are strictly increasing (contiguous within the stream's frames).
	for i := 1; i < len(stdoutSeqs); i++ {
		if stdoutSeqs[i] <= stdoutSeqs[i-1] {
			t.Fatalf("stdout seqs not increasing: %v", stdoutSeqs)
		}
	}
}

// TestStreamThrottleNoByteLoss writes a high volume of stdout with a tiny throttle
// threshold so the dynamic throttle engages, and asserts every byte still arrives
// (throttling only spaces out reads, it never drops data).
func TestStreamThrottleNoByteLoss(t *testing.T) {
	setThrottleBytes(t, 256) // throttle after 256 new bytes in a poll
	setSSEFrameCap(t, 1<<20) // keep frames whole; we test volume, not splitting

	s := newTestServer(t, testToken, false)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	id := createStreamJob(t, srv.URL, []string{"sh", "-c", "for i in $(seq 1 200); do printf 'ROW%03d-PADDINGPADDING\\n' $i; done"})
	final := waitDoneHTTP(t, srv.URL, id)
	if final.Status != job.StatusDone {
		t.Fatalf("setup: status=%q, want done", final.Status)
	}
	want := fetchLog(t, srv.URL, id)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, scanner := openStream(t, ctx, srv.URL, id, "")
	defer resp.Body.Close()

	frames := readFrames(t, resp, scanner, 10*time.Second)
	got, _, _ := collectStdout(frames)
	if got != want {
		t.Fatalf("throttled stream lost/altered bytes:\n got %d bytes\nwant %d bytes", len(got), len(want))
	}
}

// TestTailFromDetectsRotation unit-tests the rotation signal: once the live file
// shrinks below the caller's offset, tailFrom reports rotated=true (empty chunk),
// and a subsequent read from offset 0 returns the fresh content. This is the
// exact protocol the SSE loop uses to emit a `log-rotated` marker and reset.
func TestTailFromDetectsRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, store.StdoutFile)

	if err := os.WriteFile(path, []byte("AAAAAAAAAA"), 0o644); err != nil { // 10 bytes
		t.Fatal(err)
	}
	chunk, next, rotated := tailFrom(path, 0)
	if rotated || string(chunk) != "AAAAAAAAAA" || next != 10 {
		t.Fatalf("initial read: chunk=%q next=%d rotated=%v", chunk, next, rotated)
	}

	// Simulate a rotation: the live file is replaced by a smaller fresh file.
	if err := os.WriteFile(path, []byte("BBB"), 0o644); err != nil { // 3 bytes < offset 10
		t.Fatal(err)
	}
	// Reading from the stale offset must flag rotation and return no bytes.
	chunk, _, rotated = tailFrom(path, next)
	if !rotated {
		t.Fatalf("expected rotated=true when file shrank below offset")
	}
	if len(chunk) != 0 {
		t.Fatalf("rotated read must return empty chunk, got %q", chunk)
	}
	// Re-reading from 0 yields the fresh file's content with no bleed of the old tail.
	chunk, next, rotated = tailFrom(path, 0)
	if rotated || string(chunk) != "BBB" || next != 3 {
		t.Fatalf("post-rotation read: chunk=%q next=%d rotated=%v", chunk, next, rotated)
	}
}

// TestStreamRotationMarkerMidStream drives a live stream, rotates the underlying
// stdout.log mid-stream (shrink it), and asserts the SSE loop emits a
// `log-rotated` marker, resets, and delivers the post-rotation content without
// bleeding the pre-rotation tail.
func TestStreamRotationMarkerMidStream(t *testing.T) {
	setSSEFrameCap(t, 1<<20)

	s := newTestServer(t, testToken, false)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// A long-running job so the live ticker loop stays active while we rotate the
	// file out from under it. We drive the log file directly (the runner writes
	// nothing to stdout here), exercising the SSE read path deterministically.
	id := createStreamJob(t, srv.URL, []string{"sleep", "30"})
	t.Cleanup(func() {
		_ = s.jobs.Cancel(id)
		s.jobs.Wait(id)
	})

	// Wait until the runner has started and opened (O_TRUNC'd) the stdout log, so
	// our manual pre-rotation write is not clobbered by the runner's open.
	waitRunning(t, s, id)
	res, ok := s.jobs.Get(id)
	if !ok {
		t.Fatalf("job %s not live", id)
	}
	base := filepath.Dir(res.ResultDir)
	stdoutPath := filepath.Join(base, id, store.StdoutFile)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(stdoutPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stdout.log not created by runner in time")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Pre-rotation content, written after the runner opened the file (so it is not
	// truncated away) and before the stream connects so the first poll delivers it.
	if err := os.WriteFile(stdoutPath, []byte("OLD-TAIL-CONTENT\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, scanner := openStream(t, ctx, srv.URL, id, "")
	defer resp.Body.Close()

	// Collect frames in the background. A watchdog closes the body after a hard
	// deadline so the blocking Scan unblocks even if no further frames arrive.
	type collected struct {
		sawOld, sawRotated, sawNew bool
		oldBeforeRotated           bool
		newBleedsOld               bool
		done                       bool
	}
	resCh := make(chan collected, 1)
	watchdog := time.AfterFunc(8*time.Second, func() { resp.Body.Close() })
	defer watchdog.Stop()
	go func() {
		var c collected
		var newText strings.Builder
		rotatedFired := false
		for scanner.Scan() {
			raw := strings.TrimSpace(scanner.Text())
			if raw == "" {
				continue
			}
			ev := parseFrame(raw)
			switch ev.Event {
			case "log":
				var lf logFrame
				if json.Unmarshal([]byte(ev.Data), &lf) != nil || lf.Stream != "stdout" {
					continue
				}
				if !rotatedFired {
					if strings.Contains(lf.Text, "OLD-TAIL") {
						c.sawOld = true
					}
				} else {
					newText.WriteString(lf.Text)
				}
			case "log-rotated":
				var rf rotatedFrame
				if json.Unmarshal([]byte(ev.Data), &rf) == nil && rf.Stream == "stdout" {
					c.sawRotated = true
					rotatedFired = true
					if c.sawOld {
						c.oldBeforeRotated = true
					}
				}
			}
			if rotatedFired && strings.Contains(newText.String(), "NEW-FRESH") {
				c.done = true
				break
			}
		}
		nt := newText.String()
		c.sawNew = strings.Contains(nt, "NEW-FRESH")
		c.newBleedsOld = strings.Contains(nt, "OLD-TAIL")
		resCh <- c
	}()

	// Let the loop deliver the old content over a couple of polls, then rotate by
	// replacing the file with a smaller fresh one (shrinks below the read offset).
	time.Sleep(3 * streamPollInterval)
	if err := os.WriteFile(stdoutPath, []byte("NEW-FRESH\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := <-resCh
	if !c.sawOld {
		t.Fatalf("never received pre-rotation OLD content")
	}
	if !c.sawRotated {
		t.Fatalf("never received log-rotated marker after shrinking the file")
	}
	if !c.oldBeforeRotated {
		t.Fatalf("rotation marker arrived before old content")
	}
	if !c.sawNew {
		t.Fatalf("never received post-rotation NEW content")
	}
	if c.newBleedsOld {
		t.Fatalf("post-rotation content bled the pre-rotation tail")
	}
}
