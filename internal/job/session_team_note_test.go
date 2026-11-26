package job

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// FakeTeamNoter answers a fixed block and records the turns it was told began and ended.
type FakeTeamNoter struct {
	mu    sync.Mutex
	Block string
	Began [][]string
	Ended [][]string
}

func (f *FakeTeamNoter) TurnNote(_ i18n.Lang, ids ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Began = append(f.Began, ids)
	return f.Block
}

func (f *FakeTeamNoter) PeekNote(_ i18n.Lang, _ ...string) string { return f.Block }

func (f *FakeTeamNoter) TurnEnded(ids ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Ended = append(f.Ended, ids)
}

func (f *FakeTeamNoter) counts() (began, ended int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Began), len(f.Ended)
}

// A local resident session on a leader seat takes the team block in front of every turn (before
// the stage line and the person's text) and the end of each turn is reported once.
func TestLocalSessionTurnsCarryTheTeamBlock(t *testing.T) {
	const block = "[gofer · team] TEAM-BLOCK-LINE\n\n"
	s := newACPService(t, t.TempDir(), acptest.Options{EchoPrompt: true})
	noter := &FakeTeamNoter{Block: block}
	s.SetTeamNotes(noter)
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
	if n := strings.Count(stdout, "TEAM-BLOCK-LINE"); n != 2 {
		t.Fatalf("team block appears %d times, want once per turn (2):\n%s", n, stdout)
	}
	for _, text := range []string{"first message", "second message"} {
		if !strings.Contains(stdout, block+text) {
			t.Fatalf("the team block must sit right before %q:\n%s", text, stdout)
		}
	}
	if began, ended := noter.counts(); began != 2 || ended != 2 {
		t.Fatalf("turns began %d / ended %d, want 2 / 2", began, ended)
	}
	for _, ids := range noter.Began {
		if len(ids) == 0 || ids[0] != j.ID {
			t.Fatalf("the lookup must lead with the job id: %v", noter.Began)
		}
	}
}

// No team noter (the worker's own service) or one that knows no leader: no change at all.
func TestSessionWithoutTeamNotesGetsNoBlock(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{EchoPrompt: true})
	s.SetTeamNotes(&FakeTeamNoter{})
	j, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		Prompt: "first message", Session: true, TimeoutSec: 30, IdleTimeoutSec: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Cancel(j.ID) })
	cur := waitAwaitingInput(t, s, j.ID, 1)
	if stdout := readJobLog(t, cur, store.StdoutFile); strings.Contains(stdout, "[gofer · team]") {
		t.Fatalf("no leader, no block:\n%s", stdout)
	}
}
