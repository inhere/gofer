package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// TestSessionStuckOnQuestionRecoversAfterServeRestart: a local session left in
// pending_interaction by a dead serve is reloaded into awaiting_input, its old question
// is closed as stale (not left pending), the transcript carries the note the timeline
// shows, and the session can be talked to and ended afterwards.
func TestSessionStuckOnQuestionRecoversAfterServeRestart(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{})
	seedRecoverableSession(t, s, root, "stuck-ask", StatusPendingInteraction)
	if err := s.meta.UpsertInteraction(jobstore.InteractionRecord{
		ID: "i-old", JobID: "stuck-ask", Type: InteractionTypeQuestion, Prompt: "continue?",
		Status: InteractionPending, CreatedAt: time.Now().Unix() - 5,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileOrphanJobs(); err != nil {
		t.Fatal(err)
	}
	wait.For(t, 10*time.Second, "session back at awaiting_input", func() (bool, any) {
		res, _ := s.Get("stuck-ask")
		return s.entry("stuck-ask") != nil && res.Status == StatusAwaitingInput, res.Status
	})
	list, err := s.meta.ListInteractions("stuck-ask")
	if err != nil || len(list) != 1 || list[0].Status != InteractionCancelled {
		t.Fatalf("old question must be closed: err=%v list=%+v", err, list)
	}
	if pending, _ := s.ListPendingInteractions(); len(pending) != 0 {
		t.Fatalf("a dead question is still offered for answering: %+v", pending)
	}
	note, err := os.ReadFile(filepath.Join(root, "self", "stuck-ask", "artifacts", "acp.jsonl"))
	if err != nil || !strings.Contains(string(note), `"t":"session_note"`) || !strings.Contains(string(note), staleInteractionReason) {
		t.Fatalf("transcript lacks the stale-question note: err=%v %q", err, note)
	}
	if err := s.SaySession("stuck-ask", "next"); err != nil {
		t.Fatal(err)
	}
	waitSessionTurn(t, s, "stuck-ask", 2)
	if err := s.EndSession("stuck-ask"); err != nil {
		t.Fatal(err)
	}
	if final, _ := s.Wait("stuck-ask"); final.Status != StatusDone {
		t.Fatalf("final=%+v", final)
	}
}

// TestNonSessionPendingInteractionFailsAsOrphan: a local job parked on a question with
// no session state to reload would hang forever (end 409, cancel a no-op); the restart
// fails it as interrupted and closes its question.
func TestNonSessionPendingInteractionFailsAsOrphan(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{})
	now := time.Now().Unix()
	if err := s.meta.UpsertJob(jobstore.JobRecord{
		ID: "plain-ask", ProjectKey: "self", Agent: "acpbot", Runner: "local",
		Status: StatusPendingInteraction, Cwd: root, ResultDir: filepath.Join(root, "self", "plain-ask"),
		StartedAt: now - 10, UpdatedAt: now - 10,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.meta.UpsertInteraction(jobstore.InteractionRecord{
		ID: "i-plain", JobID: "plain-ask", Type: InteractionTypeQuestion, Prompt: "continue?",
		Status: InteractionPending, CreatedAt: now - 5,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileOrphanJobs(); err != nil {
		t.Fatal(err)
	}
	res, ok := s.Get("plain-ask")
	if !ok || res.Status != StatusFailed || !strings.HasPrefix(res.Error, orphanedJobErrorPrefix) {
		t.Fatalf("orphan not failed: ok=%v %+v", ok, res)
	}
	list, err := s.GetInteractions("plain-ask")
	if err != nil || len(list) != 1 || list[0].Status != InteractionCancelled {
		t.Fatalf("question must be closed: err=%v list=%+v", err, list)
	}
	if err := s.Cancel("plain-ask"); err != nil {
		t.Fatalf("cancel on the failed row must not hang or error: %v", err)
	}
}
