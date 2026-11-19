package job

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/needsreply"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// awaitingEvents returns the detail of every job.awaiting_input event of a job, oldest first.
func awaitingEvents(t *testing.T, s *Service, id string) []map[string]any {
	t.Helper()
	evs, err := s.Meta().ListJobEvents(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, ev := range evs {
		if ev.Type != EventJobAwaitingInput {
			continue
		}
		d := map[string]any{}
		_ = json.Unmarshal([]byte(ev.Detail), &d)
		out = append(out, d)
	}
	return out
}

func startNeedsReplySession(t *testing.T, tail string) (*Service, JobResult) {
	t.Helper()
	s := newACPService(t, t.TempDir(), acptest.Options{FinalTail: tail, EchoPrompt: true})
	j, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		Prompt: "first message", Session: true, TimeoutSec: 30, IdleTimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("submit session: %v", err)
	}
	var cur JobResult
	wait.Until(t, 10*time.Second, "session awaiting input", func() bool {
		cur, _ = s.Get(j.ID)
		return cur.Status == StatusAwaitingInput && len(awaitingEvents(t, s, j.ID)) > 0
	})
	t.Cleanup(func() { _ = s.Cancel(j.ID) })
	return s, cur
}

func TestSessionTurnWithMarkerIsRecordedAndHidden(t *testing.T) {
	s, cur := startNeedsReplySession(t, "\n"+needsreply.Marker)
	evs := awaitingEvents(t, s, cur.ID)
	if got := evs[len(evs)-1]["needs_reply"]; got != true {
		t.Fatalf("awaiting_input detail = %v, want needs_reply=true", evs[len(evs)-1])
	}
	stdout := readJobLog(t, cur, store.StdoutFile)
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) == needsreply.Marker {
			t.Fatalf("the marker line reached stdout.log:\n%s", stdout)
		}
	}
	if !strings.Contains(stdout, "Done.") {
		t.Fatalf("the reply text was lost:\n%s", stdout)
	}
}

func TestSessionTurnWithoutMarkerIsNotRecorded(t *testing.T) {
	s, cur := startNeedsReplySession(t, "")
	evs := awaitingEvents(t, s, cur.ID)
	if _, has := evs[len(evs)-1]["needs_reply"]; has {
		t.Fatalf("awaiting_input detail = %v, want no needs_reply", evs[len(evs)-1])
	}
}

func TestSessionConventionIsGivenOnceToTheFirstTurn(t *testing.T) {
	s, cur := startNeedsReplySession(t, "")
	if err := s.SaySession(cur.ID, "second message"); err != nil {
		t.Fatal(err)
	}
	wait.Until(t, 10*time.Second, "second turn", func() bool {
		r, _ := s.Get(cur.ID)
		return r.Status == StatusAwaitingInput && r.TurnNo == 2
	})
	stdout := readJobLog(t, cur, store.StdoutFile)
	if n := strings.Count(stdout, needsreply.Marker); n != 1 {
		t.Fatalf("convention text appears %d times in the echoed prompts, want 1 (first turn only):\n%s", n, stdout)
	}
}
