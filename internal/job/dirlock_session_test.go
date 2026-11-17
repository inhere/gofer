package job

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// R8: a persistent session gives its directory lock back while it waits for input and
// takes it again before the next turn starts. The conflict rules of the lock table are
// not touched; the tests drive the table through its ordinary Acquire.

func startDirSession(t *testing.T, keep bool) (*Service, string, JobResult) {
	t.Helper()
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{})
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		Prompt: "first message", Session: true, TimeoutSec: 60, IdleTimeoutSec: 120,
		KeepDirLock: keep,
	})
	if err != nil {
		t.Fatalf("submit session: %v", err)
	}
	waitStatus(t, s, res.ID, 15*time.Second, StatusAwaitingInput)
	return s, root, res
}

func dirEvents(t *testing.T, s *Service, id, kind string) []map[string]any {
	t.Helper()
	evs, err := s.ListJobEvents(id, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	var out []map[string]any
	for _, e := range evs {
		if e.Type != kind {
			continue
		}
		m := map[string]any{}
		_ = json.Unmarshal([]byte(e.Detail), &m)
		out = append(out, m)
	}
	return out
}

// A writable job in the same directory starts at once while the session awaits input.
func TestSessionAwaitingInputYieldsDirLock(t *testing.T) {
	s, _, sess := startDirSession(t, false)
	other, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		Prompt: "second", TimeoutSec: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitStatus(t, s, other.ID, 15*time.Second, StatusDone, StatusFailed)
	if done.Status != StatusDone {
		t.Fatalf("other job = %s (%s)", done.Status, done.Error)
	}
	if _, waited := waitingDirEvent(t, s, other.ID); waited {
		t.Fatal("the other job queued although the session was only awaiting input")
	}
	if cur, _ := s.Get(sess.ID); cur.Status != StatusAwaitingInput {
		t.Fatalf("session = %s, want awaiting_input", cur.Status)
	}
	_ = s.EndSession(sess.ID)
	s.Wait(sess.ID)
}

// keep_dir_lock keeps the old behaviour: the writable job queues behind the session.
func TestSessionKeepDirLockHoldsWhileAwaiting(t *testing.T) {
	s, _, sess := startDirSession(t, true)
	other, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		Prompt: "second", TimeoutSec: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	w := waitStatus(t, s, other.ID, 15*time.Second, StatusWaitingDir)
	if w.WaitingOnJob != sess.ID {
		t.Fatalf("waiting_on_job = %q, want %q", w.WaitingOnJob, sess.ID)
	}
	if err := s.EndSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	s.Wait(sess.ID)
	if f := waitStatus(t, s, other.ID, 15*time.Second, StatusDone, StatusFailed); f.Status != StatusDone {
		t.Fatalf("other = %s (%s)", f.Status, f.Error)
	}
}

// The next turn does not start while someone else holds the directory: the job stays
// awaiting_input with a job.dir_wait event, starts after the holder left, and the
// prompt carries the note about who used the directory.
func TestSessionNextTurnWaitsForDirLockThenNotes(t *testing.T) {
	s, root, sess := startDirSession(t, false)
	rel, _, _, err := s.dirLock.Acquire(context.Background(), []string{root}, "j-other", nil)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			rel()
		}
	}()
	if err := s.SaySession(sess.ID, "second message"); err != nil {
		t.Fatal(err)
	}
	wait.Until(t, 10*time.Second, "job.dir_wait event", func() bool { return len(dirEvents(t, s, sess.ID, EventJobDirWait)) > 0 })
	ev := dirEvents(t, s, sess.ID, EventJobDirWait)[0]
	if ev["holder_job"] != "j-other" {
		t.Fatalf("dir_wait detail = %v", ev)
	}
	time.Sleep(200 * time.Millisecond)
	cur, _ := s.Get(sess.ID)
	if cur.Status != StatusAwaitingInput || cur.TurnNo != 1 {
		t.Fatalf("while queued the session = %s turn %d, want awaiting_input turn 1", cur.Status, cur.TurnNo)
	}
	rel()
	released = true
	wait.Until(t, 15*time.Second, "turn 2 done", func() bool {
		c, _ := s.Get(sess.ID)
		return c.Status == StatusAwaitingInput && c.TurnNo == 2
	})
	cur, _ = s.Get(sess.ID)
	acpLog, err := os.ReadFile(filepath.Join(cur.ResultDir, "artifacts", "acp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(acpLog), "second message") {
		t.Fatalf("turn 2 prompt missing from acp.jsonl")
	}
	// the holder was granted the directory while the session had given it back: the
	// prompt of turn 2 names it in the injected note.
	if !strings.Contains(string(acpLog), "j-other") {
		t.Fatalf("turn 2 prompt lacks the note naming the job that used the directory")
	}
	_ = s.EndSession(sess.ID)
	s.Wait(sess.ID)
}

// A job that ran in the directory while the lock was given back is named in the note
// that precedes the next turn's prompt.
func TestSessionNextTurnNoteNamesJobsThatUsedTheDir(t *testing.T) {
	s, _, sess := startDirSession(t, false)
	other, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		Prompt: "second", Title: "side change", TimeoutSec: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s, other.ID, 15*time.Second, StatusDone)
	s.Wait(other.ID)
	if err := s.SaySession(sess.ID, "third message"); err != nil {
		t.Fatal(err)
	}
	wait.Until(t, 15*time.Second, "turn 2", func() bool {
		c, _ := s.Get(sess.ID)
		return c.Status == StatusAwaitingInput && c.TurnNo == 2
	})
	cur, _ := s.Get(sess.ID)
	raw, _ := os.ReadFile(filepath.Join(cur.ResultDir, "artifacts", "acp.jsonl"))
	log := string(raw)
	if !strings.Contains(log, other.ID) || !strings.Contains(log, "side change") || !strings.Contains(log, "gofer:rules-end") {
		t.Fatalf("turn 2 prompt lacks the injected note naming %s:\n%s", other.ID, log)
	}
	_ = s.EndSession(sess.ID)
	s.Wait(sess.ID)
}

// end takes effect at once on a session queued for its directory lock.
func TestSessionEndWhileWaitingForDirLock(t *testing.T) {
	s, root, sess := startDirSession(t, false)
	rel, _, _, err := s.dirLock.Acquire(context.Background(), []string{root}, "j-other", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	if err := s.SaySession(sess.ID, "second message"); err != nil {
		t.Fatal(err)
	}
	wait.Until(t, 10*time.Second, "job.dir_wait", func() bool { return len(dirEvents(t, s, sess.ID, EventJobDirWait)) > 0 })
	if err := s.EndSession(sess.ID); err != nil {
		t.Fatalf("end while queued: %v", err)
	}
	final, _ := s.Wait(sess.ID)
	if final.Status != StatusDone {
		t.Fatalf("ended session = %s (%s), want done", final.Status, final.Error)
	}
}

// cancel takes effect at once on a session queued for its directory lock.
func TestSessionCancelWhileWaitingForDirLock(t *testing.T) {
	s, root, sess := startDirSession(t, false)
	rel, _, _, err := s.dirLock.Acquire(context.Background(), []string{root}, "j-other", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	if err := s.SaySession(sess.ID, "second message"); err != nil {
		t.Fatal(err)
	}
	wait.Until(t, 10*time.Second, "job.dir_wait", func() bool { return len(dirEvents(t, s, sess.ID, EventJobDirWait)) > 0 })
	if err := s.Cancel(sess.ID); err != nil {
		t.Fatal(err)
	}
	final, _ := s.Wait(sess.ID)
	if final.Status != StatusCancelled {
		t.Fatalf("cancelled session = %s (%s)", final.Status, final.Error)
	}
}
