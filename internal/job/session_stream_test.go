package job

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// nopWriteCloser adapts a bytes.Buffer to the io.WriteCloser the observer writer wraps.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// textSessionID is the session id the F-e tests use — the same uuid shape codex
// prints in its batch header (`session id: <uuid>`).
const textSessionID = "9f2c1a44-7b6e-4d3a-9f01-2c5b7e0d1a88"

// TestTextSessionIDPersistedWhenSeen pins F-e: a NON-ndjson cli-agent whose text
// output carries a session id gets that id on the job ROW while the job is still
// running (not only at the terminal log scan), and the observation stays bounded —
// a 1MB stream is never rescanned end to end.
func TestTextSessionIDPersistedWhenSeen(t *testing.T) {
	t.Parallel()
	t.Run("persists_while_running", func(t *testing.T) {
		root := t.TempDir()
		// The fake codex prints its session-id header and then KEEPS RUNNING, so the
		// read below happens mid-run — exactly the window a serve restart kills.
		s := newServiceFromCfg(t, root, &config.Config{
			Storage: config.StorageConfig{Root: root},
			Projects: map[string]config.ProjectConfig{
				"self": {
					HostPath:       root,
					AllowedAgents:  []string{"codex", "exec"},
					AllowedRunners: []string{"local"},
					AllowExec:      true,
				},
			},
			Agents: map[string]config.AgentConfig{
				"codex": {
					Type:    agent.TypeCLIAgent,
					Command: testcmd.Path(t),
					// stdout-sleep prints the header line, then idles: the id has to be
					// readable long before the process exits.
					Args: []string{"stdout-sleep", "session id: " + textSessionID, "60s"},
				},
			},
		})

		res, err := s.Submit(JobRequest{
			ProjectKey: "self", Agent: "codex", Runner: "local",
			Prompt: "hi", Cwd: ".", TimeoutSec: 30,
		})
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		t.Cleanup(func() { _ = s.Cancel(res.ID); s.Wait(res.ID) })

		rec := waitJobSessionID(t, s, res.ID, 10*time.Second)
		if rec.SessionID != textSessionID {
			t.Fatalf("persisted session_id = %q, want %q", rec.SessionID, textSessionID)
		}
		if rec.Status != StatusRunning {
			t.Fatalf("session_id landed with status=%s, want it persisted WHILE the job runs", rec.Status)
		}
		assertSessionCapturedEvent(t, s, res.ID, `"agent":"codex"`, `"by":"agent_config"`, `"source":"stream"`)
	})

	t.Run("large_output_is_not_rescanned", func(t *testing.T) {
		cap := newStreamSessionCapture("j-big", "codex", `(?i)session id:\s*([0-9a-f-]{36})`, func(string) {})
		// 1MB of output with NO session id, written in 4KB chunks — the shape of a
		// chatty agent. The observer must scan O(input) bytes, never the accumulated
		// buffer per write (which would be ~128MB here), and its windows must stay
		// bounded regardless of how much was written.
		const chunk = 4 << 10
		const total = 1 << 20
		line := []byte(strings.Repeat("noise ", 600) + "\n")
		var sink bytes.Buffer
		w := streamSessionCaptureWriter{w: nopWriteCloser{&sink}, cap: cap}
		for written := 0; written < total; written += chunk {
			if _, err := w.Write(line); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
		if cap.hit {
			t.Fatal("a filler stream must not produce a session id")
		}
		if cap.scanned > 2*(1<<20)+streamOverlap*4 {
			t.Fatalf("scanned %d bytes for a 1MB stream — the observer is rescanning history", cap.scanned)
		}
		if len(cap.head) > streamHeadBytes {
			t.Fatalf("head window = %d bytes, want <= %d", len(cap.head), streamHeadBytes)
		}
		if len(cap.tail) > streamTailBytes {
			t.Fatalf("tail window = %d bytes, want <= %d", len(cap.tail), streamTailBytes)
		}
	})

	t.Run("captures_from_the_tail_window", func(t *testing.T) {
		// The id arrives AFTER more than the head window has been written: the rolling
		// tail must still catch it (a head-only observer would miss it entirely).
		var got string
		cap := newStreamSessionCapture("j-tail", "codex", `(?i)session id:\s*([0-9a-f-]{36})`, func(sid string) { got = sid })
		w := streamSessionCaptureWriter{w: nopWriteCloser{&bytes.Buffer{}}, cap: cap}
		filler := bytes.Repeat([]byte("x"), streamHeadBytes+1024)
		for len(filler) > 0 {
			n := len(filler)
			if n > 4096 {
				n = 4096
			}
			if _, err := w.Write(filler[:n]); err != nil {
				t.Fatalf("write filler: %v", err)
			}
			filler = filler[n:]
		}
		if _, err := w.Write([]byte(fmt.Sprintf("session id: %s\n", textSessionID))); err != nil {
			t.Fatalf("write banner: %v", err)
		}
		if got != textSessionID {
			t.Fatalf("captured = %q, want %q from the tail window", got, textSessionID)
		}
	})
}
