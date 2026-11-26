package job

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/needsreply"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// fakeStageNoter answers a fixed line (when it has one) and records every lookup.
type fakeStageNoter struct {
	mu   sync.Mutex
	line string
	seen [][]string
}

func (f *fakeStageNoter) TurnNote(_ i18n.Lang, sids ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, sids)
	return f.line
}

// A local resident session takes the stage line in front of EVERY turn it runs (not only the
// first, unlike the needs-reply convention), after that convention and right before the
// person's text, once per turn.
func TestLocalSessionTurnsCarryTheStageLine(t *testing.T) {
	const line = "[gofer · 流程] STAGE-NOTE-LINE"
	s := newACPService(t, t.TempDir(), acptest.Options{EchoPrompt: true})
	noter := &fakeStageNoter{line: line}
	s.SetStageNotes(noter)
	j, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		Prompt: "first message", Session: true, TimeoutSec: 30, IdleTimeoutSec: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Cancel(j.ID) })
	waitAwaitingInput(t, s, j.ID, 1)
	if err := s.SaySession(j.ID, "second message"); err != nil {
		t.Fatal(err)
	}
	wait.Until(t, 10*time.Second, "second turn", func() bool {
		r, _ := s.Get(j.ID)
		return r.Status == StatusAwaitingInput && r.TurnNo == 2
	})
	cur, _ := s.Get(j.ID)
	stdout := readJobLog(t, cur, store.StdoutFile)
	if n := strings.Count(stdout, "STAGE-NOTE-LINE"); n != 2 {
		t.Fatalf("stage line appears %d times, want once per turn (2):\n%s", n, stdout)
	}
	for _, text := range []string{"first message", "second message"} {
		if !strings.Contains(stdout, line+"\n\n"+text) {
			t.Fatalf("the stage line must sit right before the person's text %q:\n%s", text, stdout)
		}
	}
	if n := strings.Count(stdout, needsreply.Marker); n != 1 {
		t.Fatalf("needs-reply convention appears %d times, want 1:\n%s", n, stdout)
	}
	noter.mu.Lock()
	defer noter.mu.Unlock()
	for _, ids := range noter.seen {
		if len(ids) == 0 || ids[0] != j.ID {
			t.Fatalf("the lookup must lead with the job id: %v", noter.seen)
		}
	}
}

// With no noter injected (the worker's own service) a session turn gets no stage line.
func TestSessionWithoutStageNotesGetsNoLine(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{EchoPrompt: true})
	j, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		Prompt: "first message", Session: true, TimeoutSec: 30, IdleTimeoutSec: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Cancel(j.ID) })
	cur := waitAwaitingInput(t, s, j.ID, 1)
	if stdout := readJobLog(t, cur, store.StdoutFile); strings.Contains(stdout, "gofer stage") || strings.Contains(stdout, "流程]") {
		t.Fatalf("no noter, no line:\n%s", stdout)
	}
}
