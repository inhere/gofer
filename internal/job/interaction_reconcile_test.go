package job

import (
	"context"
	"testing"
	"time"
)

// waitStatus polls until the job reaches one of the wanted statuses or times out.
func waitStatus(t *testing.T, s *Service, id string, deadline time.Duration, want ...string) JobResult {
	t.Helper()
	stop := time.Now().Add(deadline)
	for time.Now().Before(stop) {
		res, ok := s.Get(id)
		if ok {
			for _, w := range want {
				if res.Status == w {
					return res
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach %v in time", id, want)
	return JobResult{}
}

// TestFinishReconcilesPendingInteraction proves a job that is cancelled while an
// interaction is pending has that interaction flipped to cancelled (not left a
// 僵尸 pending), a WaitAnswer caller is woken with the cancelled snapshot (no
// hang), and ListPendingInteractions no longer reports it (E25, 复审 #4).
func TestFinishReconcilesPendingInteraction(t *testing.T) {
	root := t.TempDir()
	s := newClaudeInjectService(t, root) // allows exec + allow_exec

	// A long-lived exec job so we can raise an interaction while it is running.
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "30"}, Cwd: ".", TimeoutSec: 60,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitStatus(t, s, res.ID, 5*time.Second, StatusRunning)

	it, err := s.CreateInteraction(res.ID, InteractionInput{Type: InteractionTypeQuestion, Prompt: "continue?"})
	if err != nil {
		t.Fatalf("CreateInteraction: %v", err)
	}

	// A WaitAnswer caller must be woken (with cancelled), not hang forever.
	waitDone := make(chan Interaction, 1)
	go func() {
		got, _ := s.WaitAnswer(context.Background(), res.ID, it.ID)
		waitDone <- got
	}()

	// Cancel the job → finish() runs the terminal reconciliation.
	if err := s.Cancel(res.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	final, _ := s.Wait(res.ID)
	if !IsTerminal(final.Status) {
		t.Fatalf("job not terminal after cancel: %s", final.Status)
	}

	// WaitAnswer woke with the cancelled interaction.
	select {
	case got := <-waitDone:
		if got.Status != InteractionCancelled {
			t.Fatalf("WaitAnswer returned status %q, want cancelled", got.Status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WaitAnswer hung after job finished (reconciliation did not close the channel)")
	}

	// The persisted interaction is cancelled (read falls back to DB after eviction).
	list, err := s.GetInteractions(res.ID)
	if err != nil {
		t.Fatalf("GetInteractions: %v", err)
	}
	if len(list) != 1 || list[0].Status != InteractionCancelled {
		t.Fatalf("interaction not reconciled to cancelled: %+v", list)
	}

	// And it is no longer reported as a pending interaction anywhere.
	pending, err := s.ListPendingInteractions()
	if err != nil {
		t.Fatalf("ListPendingInteractions: %v", err)
	}
	for _, p := range pending {
		if p.ID == it.ID {
			t.Fatalf("cancelled interaction still listed as pending: %+v", p)
		}
	}
}
